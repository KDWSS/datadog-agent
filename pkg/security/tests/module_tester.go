// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//+build functionaltests stresstests

package tests

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"text/template"
	"time"
	"unsafe"

	"github.com/cihub/seelog"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/unix"

	sysconfig "github.com/DataDog/datadog-agent/cmd/system-probe/config"
	"github.com/DataDog/datadog-agent/pkg/process/util"
	"github.com/DataDog/datadog-agent/pkg/security/config"
	"github.com/DataDog/datadog-agent/pkg/security/module"
	sprobe "github.com/DataDog/datadog-agent/pkg/security/probe"
	"github.com/DataDog/datadog-agent/pkg/security/secl/compiler/eval"
	"github.com/DataDog/datadog-agent/pkg/security/secl/model"
	"github.com/DataDog/datadog-agent/pkg/security/secl/rules"
	"github.com/DataDog/datadog-agent/pkg/security/utils"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

var (
	logger seelog.LoggerInterface
)

const (
	getEventTimeout = 10 * time.Second
)

type stringSlice []string

const testConfig = `---
log_level: DEBUG
system_probe_config:
  enabled: true
  sysprobe_socket: /tmp/test-sysprobe.sock

runtime_security_config:
  enabled: true
  fim_enabled: true
  remote_tagger: false
  custom_sensitive_words:
    - "*custom*"
  socket: /tmp/test-security-probe.sock
  flush_discarder_window: 0
  load_controller:
    events_count_threshold: {{ .EventsCountThreshold }}
{{if .DisableFilters}}
  enable_kernel_filters: false
{{end}}
{{if .DisableApprovers}}
  enable_approvers: false
{{end}}
  erpc_dentry_resolution_enabled: {{ .ErpcDentryResolutionEnabled }}
  map_dentry_resolution_enabled: {{ .MapDentryResolutionEnabled }}

  policies:
    dir: {{.TestPoliciesDir}}
  log_patterns:
  {{range .LogPatterns}}
    - {{.}}
  {{end}}
`

const testPolicy = `---
macros:
{{range $Macro := .Macros}}
  - id: {{$Macro.ID}}
    expression: >-
      {{$Macro.Expression}}
{{end}}

rules:
{{range $Rule := .Rules}}
  - id: {{$Rule.ID}}
    expression: >-
      {{$Rule.Expression}}
{{end}}
`

var (
	testEnvironment string
	useReload       bool
	logLevelStr     string
	logPatterns     stringSlice
)

const (
	HostEnvironment   = "host"
	DockerEnvironment = "docker"
)

type testOpts struct {
	testDir                     string
	disableFilters              bool
	disableApprovers            bool
	disableDiscarders           bool
	eventsCountThreshold        int
	reuseProbeHandler           bool
	disableERPCDentryResolution bool
	disableMapDentryResolution  bool
}

func (s *stringSlice) String() string {
	return strings.Join(*s, " ")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func (to testOpts) Equal(opts testOpts) bool {
	return to.testDir == opts.testDir &&
		to.disableApprovers == opts.disableApprovers &&
		to.disableDiscarders == opts.disableDiscarders &&
		to.disableFilters == opts.disableFilters &&
		to.eventsCountThreshold == opts.eventsCountThreshold &&
		to.reuseProbeHandler == opts.reuseProbeHandler &&
		to.disableERPCDentryResolution == opts.disableERPCDentryResolution &&
		to.disableMapDentryResolution == opts.disableMapDentryResolution
}

type testModule struct {
	config                *config.Config
	opts                  testOpts
	st                    *simpleTest
	module                *module.Module
	probe                 *sprobe.Probe
	probeHandler          *testProbeHandler
	cmdWrapper            cmdWrapper
	ruleHandler           testRuleHandler
	eventDiscarderHandler testEventDiscarderHandler
}

var testMod *testModule

type testDiscarder struct {
	event     eval.Event
	field     string
	eventType eval.EventType
}

type ruleHandler func(*sprobe.Event, *rules.Rule)
type eventHandler func(*sprobe.Event)
type customEventHandler func(*rules.Rule, *sprobe.CustomEvent)
type eventDiscarderHandler func(*testDiscarder) bool

type testEventDiscarderHandler struct {
	sync.RWMutex
	callback eventDiscarderHandler
}

type testRuleHandler struct {
	sync.RWMutex
	callback ruleHandler
}

type testEventHandler struct {
	callback eventHandler
}

type testcustomEventHandler struct {
	callback customEventHandler
}

type testProbeHandler struct {
	sync.RWMutex
	module             *module.Module
	eventHandler       *testEventHandler
	customEventHandler *testcustomEventHandler
}

func (h *testProbeHandler) HandleEvent(event *sprobe.Event) {
	h.RLock()
	defer h.RUnlock()

	if h.module == nil {
		return
	}

	h.module.HandleEvent(event)

	if h.eventHandler != nil && h.eventHandler.callback != nil {
		h.eventHandler.callback(event)
	}
}

func (h *testProbeHandler) SetModule(module *module.Module) {
	h.Lock()
	h.module = module
	h.Unlock()
}

func (h *testProbeHandler) HandleCustomEvent(rule *rules.Rule, event *sprobe.CustomEvent) {
	h.RLock()
	defer h.RUnlock()

	if h.module == nil {
		return
	}

	if h.customEventHandler != nil && h.customEventHandler.callback != nil {
		h.customEventHandler.callback(rule, event)
	}
}

//nolint:deadcode,unused
func getInode(t *testing.T, path string) uint64 {
	fileInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}

	stats, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal(errors.New("Not a syscall.Stat_t"))
	}

	return stats.Ino
}

//nolint:deadcode,unused
func which(name string) string {
	executable := "/usr/bin/" + name
	if resolved, err := os.Readlink(executable); err == nil {
		executable = resolved
	} else {
		if os.IsNotExist(err) {
			executable = "/bin/" + name
		}
	}
	return executable
}

//nolint:deadcode,unused
func copyFile(src string, dst string, mode fs.FileMode) error {
	input, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(dst, input, mode)
	if err != nil {
		return err
	}

	return nil
}

//nolint:deadcode,unused
func assertMode(t *testing.T, actualMode, expectedMode uint32, msgAndArgs ...interface{}) {
	t.Helper()
	if len(msgAndArgs) == 0 {
		msgAndArgs = append(msgAndArgs, "wrong mode")
	}
	assert.Equal(t, strconv.FormatUint(uint64(expectedMode), 8), strconv.FormatUint(uint64(actualMode), 8), msgAndArgs...)
}

//nolint:deadcode,unused
func assertRights(t *testing.T, actualMode, expectedMode uint16, msgAndArgs ...interface{}) {
	t.Helper()
	assertMode(t, uint32(actualMode)&01777, uint32(expectedMode), msgAndArgs...)
}

//nolint:deadcode,unused
func assertNearTime(t *testing.T, ns uint64) {
	t.Helper()
	now, event := time.Now(), time.Unix(0, int64(ns))
	if event.After(now) || event.Before(now.Add(-1*time.Hour)) {
		t.Errorf("expected time close to %s, got %s", now, event)
	}
}

//nolint:deadcode,unused
func assertTriggeredRule(t *testing.T, r *rules.Rule, id string) {
	t.Helper()
	assert.Equal(t, id, r.ID, "wrong triggered rule")
}

//nolint:deadcode,unused
func assertReturnValue(t *testing.T, retval, expected int64) {
	t.Helper()
	assert.Equal(t, expected, retval, "wrong return value")
}

//nolint:deadcode,unused
func assertFieldEqual(t *testing.T, e *sprobe.Event, field string, value interface{}, msgAndArgs ...interface{}) {
	t.Helper()
	fieldValue, err := e.GetFieldValue(field)
	if err != nil {
		t.Errorf("failed to get field '%s': %s", field, err)
	} else {
		assert.Equal(t, value, fieldValue, msgAndArgs...)
	}
}

//nolint:deadcode,unused
func assertFieldOneOf(t *testing.T, e *sprobe.Event, field string, values []interface{}, msgAndArgs ...interface{}) {
	t.Helper()
	fieldValue, err := e.GetFieldValue(field)
	if err != nil {
		t.Errorf("failed to get field '%s': %s", field, err)
	} else {
		assert.Contains(t, values, fieldValue)
	}
}

func setTestConfig(dir string, opts testOpts) (string, error) {
	tmpl, err := template.New("test-config").Parse(testConfig)
	if err != nil {
		return "", err
	}

	if opts.eventsCountThreshold == 0 {
		opts.eventsCountThreshold = 100000000
	}

	erpcDentryResolutionEnabled := true
	if opts.disableERPCDentryResolution {
		erpcDentryResolutionEnabled = false
	}

	mapDentryResolutionEnabled := true
	if opts.disableMapDentryResolution {
		mapDentryResolutionEnabled = false
	}

	buffer := new(bytes.Buffer)
	if err := tmpl.Execute(buffer, map[string]interface{}{
		"TestPoliciesDir":             dir,
		"DisableApprovers":            opts.disableApprovers,
		"EventsCountThreshold":        opts.eventsCountThreshold,
		"ErpcDentryResolutionEnabled": erpcDentryResolutionEnabled,
		"MapDentryResolutionEnabled":  mapDentryResolutionEnabled,
		"LogPatterns":                 logPatterns,
	}); err != nil {
		return "", err
	}

	sysprobeConfig, err := os.Create(path.Join(opts.testDir, "system-probe.yaml"))
	if err != nil {
		return "", err
	}
	defer sysprobeConfig.Close()

	_, err = io.Copy(sysprobeConfig, buffer)
	return sysprobeConfig.Name(), err
}

func setTestPolicy(dir string, macros []*rules.MacroDefinition, rules []*rules.RuleDefinition) (string, error) {
	testPolicyFile, err := ioutil.TempFile(dir, "secagent-policy.*.policy")
	if err != nil {
		return "", err
	}

	fail := func(err error) error {
		os.Remove(testPolicyFile.Name())
		return err
	}

	tmpl, err := template.New("test-policy").Parse(testPolicy)
	if err != nil {
		return "", fail(err)
	}

	buffer := new(bytes.Buffer)
	if err := tmpl.Execute(buffer, map[string]interface{}{
		"Rules":  rules,
		"Macros": macros,
	}); err != nil {
		return "", fail(err)
	}

	_, err = testPolicyFile.Write(buffer.Bytes())
	if err != nil {
		return "", fail(err)
	}

	if err := testPolicyFile.Close(); err != nil {
		return "", fail(err)
	}

	return testPolicyFile.Name(), nil
}

func newTestModule(t testing.TB, macroDefs []*rules.MacroDefinition, ruleDefs []*rules.RuleDefinition, opts testOpts) (*testModule, error) {
	logLevel, found := seelog.LogLevelFromString(logLevelStr)
	if !found {
		return nil, fmt.Errorf("invalid log level '%s'", logLevel)
	}

	st, err := newSimpleTest(macroDefs, ruleDefs, opts.testDir, logLevel)
	if err != nil {
		return nil, err
	}

	sysprobeConfig, err := setTestConfig(st.root, opts)
	if err != nil {
		return nil, err
	}

	cfgFilename, err := setTestPolicy(st.root, macroDefs, ruleDefs)
	if err != nil {
		return nil, err
	}
	defer os.Remove(cfgFilename)

	var cmdWrapper cmdWrapper
	if testEnvironment == DockerEnvironment {
		cmdWrapper = newStdCmdWrapper()
	} else {
		wrapper, err := newDockerCmdWrapper(st.Root())
		if err == nil {
			cmdWrapper = newMultiCmdWrapper(wrapper, newStdCmdWrapper())
		} else {
			// docker not present run only on host
			cmdWrapper = newStdCmdWrapper()
		}
	}

	if useReload && testMod != nil {
		if opts.Equal(testMod.opts) {
			testMod.st = st
			testMod.cmdWrapper = cmdWrapper
			return testMod, testMod.reloadConfiguration()
		}
		testMod.probeHandler.SetModule(nil)
		testMod.cleanup()
	}

	agentConfig, err := sysconfig.New(sysprobeConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create config")
	}
	config, err := config.NewConfig(agentConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create config")
	}

	config.SelfTestEnabled = false
	config.ERPCDentryResolutionEnabled = !opts.disableERPCDentryResolution
	config.MapDentryResolutionEnabled = !opts.disableMapDentryResolution

	t.Log("Instantiating a new security module")

	mod, err := module.NewModule(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create module")
	}

	if opts.disableApprovers {
		config.EnableApprovers = false
	}

	testMod = &testModule{
		config:       config,
		opts:         opts,
		st:           st,
		module:       mod.(*module.Module),
		probe:        mod.(*module.Module).GetProbe(),
		probeHandler: &testProbeHandler{module: mod.(*module.Module)},
		cmdWrapper:   cmdWrapper,
	}

	testMod.module.SetRulesetLoadedCallback(func(rs *rules.RuleSet) {
		log.Infof("Adding test module as listener")
		rs.AddListener(testMod)
	})

	if err := testMod.module.Init(); err != nil {
		return nil, errors.Wrap(err, "failed to init module")
	}

	testMod.probe.SetEventHandler(testMod.probeHandler)

	if err := testMod.module.Start(); err != nil {
		return nil, errors.Wrap(err, "failed to start module")
	}

	return testMod, nil
}

func (tm *testModule) Run(t *testing.T, name string, fnc func(t *testing.T, kind wrapperType, cmd func(bin string, args []string, envs []string) *exec.Cmd)) {
	tm.cmdWrapper.Run(t, name, fnc)
}

func (tm *testModule) reloadConfiguration() error {
	log.Debugf("reload configuration with testDir: %s", tm.Root())
	tm.config.PoliciesDir = tm.Root()

	if err := tm.module.Reload(); err != nil {
		return errors.Wrap(err, "failed to reload test module")
	}

	return nil
}

func (tm *testModule) Root() string {
	return tm.st.root
}

func (tm *testModule) SwapLogLevel(logLevel seelog.LogLevel) (seelog.LogLevel, error) {
	return tm.st.swapLogLevel(logLevel)
}

func (tm *testModule) RuleMatch(rule *rules.Rule, event eval.Event) {
	tm.ruleHandler.RLock()
	callback := tm.ruleHandler.callback
	tm.ruleHandler.RUnlock()

	if callback != nil {
		callback(event.(*sprobe.Event), rule)
	}
}

func (tm *testModule) RegisterEventDiscarderHandler(cb eventDiscarderHandler) {
	tm.eventDiscarderHandler.Lock()
	tm.eventDiscarderHandler.callback = cb
	tm.eventDiscarderHandler.Unlock()
}

func (tm *testModule) EventDiscarderFound(rs *rules.RuleSet, event eval.Event, field eval.Field, eventType eval.EventType) {
	tm.eventDiscarderHandler.RLock()
	callback := tm.eventDiscarderHandler.callback
	tm.eventDiscarderHandler.RUnlock()

	if callback != nil {
		discarder := &testDiscarder{event: event.(*sprobe.Event), field: field, eventType: eventType}
		_ = callback(discarder)
	}
}

func (tm *testModule) GetEventDiscarder(tb testing.TB, action func() error, cb eventDiscarderHandler) error {
	tb.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tm.RegisterEventDiscarderHandler(func(d *testDiscarder) bool {
		tb.Helper()
		if cb(d) {
			cancel()
		}
		return true
	})

	defer func() {
		tm.RegisterEventDiscarderHandler(nil)
	}()

	if err := action(); err != nil {
		tb.Fatal(err)
		return err
	}

	select {
	case <-time.After(getEventTimeout):
		return NewTimeoutError(tm.probe)
	case <-ctx.Done():
		return nil
	}
}

// GetStatusMetrics returns a string representation of the perf buffer monitor metrics
func GetStatusMetrics(probe *sprobe.Probe) string {
	var status string

	if probe == nil {
		return status
	}
	monitor := probe.GetMonitor()
	if monitor == nil {
		return status
	}
	perfBufferMonitor := monitor.GetPerfBufferMonitor()
	if perfBufferMonitor == nil {
		return status
	}

	status = fmt.Sprintf("%d lost", perfBufferMonitor.GetKernelLostCount("events", -1))

	for i := model.UnknownEventType; i < model.MaxEventType; i++ {
		stats, kernelStats := perfBufferMonitor.GetEventStats(i, "events", -1)
		status = fmt.Sprintf("%s, %s user:%d kernel:%d lost:%d", status, i, stats.Count, kernelStats.Count, kernelStats.Lost)
	}

	return status
}

// ErrTimeout is used to indicate that a test timed out
type ErrTimeout struct {
	msg string
}

func (et ErrTimeout) Error() string {
	return et.msg
}

// NewTimeoutError returns a new timeout error with the metrics collected during the test
func NewTimeoutError(probe *sprobe.Probe) ErrTimeout {
	err := ErrTimeout{
		"timeout, ",
	}

	err.msg += GetStatusMetrics(probe)
	return err
}

func (tm *testModule) WaitSignal(tb testing.TB, action func() error, cb ruleHandler) error {
	if err := tm.GetSignal(tb, action, cb); err != nil {
		tb.Error(err)
		return err
	}
	return nil
}

func (tm *testModule) GetSignal(tb testing.TB, action func() error, cb ruleHandler) error {
	tb.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tm.RegisterRuleEventHandler(func(e *sprobe.Event, r *rules.Rule) {
		tb.Helper()
		cb(e, r)
		cancel()
	})

	defer func() {
		tm.RegisterRuleEventHandler(nil)
	}()

	if err := action(); err != nil {
		tb.Fatal(err)
		return err
	}

	select {
	case <-time.After(getEventTimeout):
		return NewTimeoutError(tm.probe)
	case <-ctx.Done():
		return nil
	}
}

func (tm *testModule) RegisterRuleEventHandler(cb ruleHandler) {
	tm.ruleHandler.Lock()
	tm.ruleHandler.callback = cb
	tm.ruleHandler.Unlock()
}

func (tm *testModule) GetProbeCustomEvent(tb testing.TB, action func() error, cb func(rule *rules.Rule, event *sprobe.CustomEvent) bool, eventType ...model.EventType) error {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tm.RegisterCustomEventHandler(func(rule *rules.Rule, event *sprobe.CustomEvent) {
		if len(eventType) > 0 {
			if event.GetEventType() != eventType[0] {
				return
			}
		}

		if cb(rule, event) {
			cancel()
		}
	})
	defer tm.RegisterCustomEventHandler(nil)

	if err := action(); err != nil {
		return err
	}

	select {
	case <-time.After(getEventTimeout):
		return NewTimeoutError(tm.probe)
	case <-ctx.Done():
		return nil
	}
}

func (tm *testModule) RegisterEventHandler(cb eventHandler) {
	tm.probeHandler.Lock()
	tm.probeHandler.eventHandler = &testEventHandler{callback: cb}
	tm.probeHandler.Unlock()
}

func (tm *testModule) RegisterCustomEventHandler(cb customEventHandler) {
	tm.probeHandler.Lock()
	tm.probeHandler.customEventHandler = &testcustomEventHandler{callback: cb}
	tm.probeHandler.Unlock()
}

func (tm *testModule) GetProbeEvent(action func() error, cb func(event *sprobe.Event) bool, timeout time.Duration, eventTypes ...model.EventType) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tm.RegisterEventHandler(func(event *sprobe.Event) {
		if len(eventTypes) > 0 {
			match := false
			for _, eventType := range eventTypes {
				if event.GetEventType() == eventType {
					match = true
					break
				}
			}
			if !match {
				return
			}
		}

		if cb(event) {
			cancel()
		}
	})
	defer tm.RegisterEventHandler(nil)

	if action != nil {
		if err := action(); err != nil {
			return err
		}
	}

	select {
	case <-time.After(getEventTimeout):
		return NewTimeoutError(tm.probe)
	case <-ctx.Done():
		return nil
	}
}

func (tm *testModule) Path(filename ...string) (string, unsafe.Pointer, error) {
	return tm.st.Path(filename...)
}

func (tm *testModule) CreateWithOptions(filename string, user, group, mode int) (string, unsafe.Pointer, error) {
	testFile, testFilePtr, err := tm.st.Path(filename)
	if err != nil {
		return testFile, testFilePtr, err
	}

	// Create file
	f, err := os.OpenFile(testFile, os.O_CREATE, os.FileMode(mode))
	if err != nil {
		return "", nil, err
	}
	f.Close()

	// Chown the file
	err = os.Chown(testFile, user, group)
	return testFile, testFilePtr, err
}

func (tm *testModule) Create(filename string) (string, unsafe.Pointer, error) {
	testFile, testPtr, err := tm.st.Path(filename)
	if err != nil {
		return "", nil, err
	}

	f, err := os.Create(testFile)
	if err != nil {
		return "", nil, err
	}

	if err := f.Close(); err != nil {
		return "", nil, err
	}

	return testFile, testPtr, err
}

//nolint:unused
type tracePipeLogger struct {
	*TracePipe
	stop       chan struct{}
	executable string
}

//nolint:unused
func (l *tracePipeLogger) handleEvent(event *TraceEvent) {
	// for some reason, the event task is resolved to "<...>"
	// so we check that event.PID is the ID of a task of the running process
	taskPath := filepath.Join(util.HostProc(), strconv.Itoa(int(utils.Getpid())), "task", event.PID)
	_, err := os.Stat(taskPath)

	if event.Task == l.executable || (event.Task == "<...>" && err == nil) {
		log.Debug(event.Raw)
	}
}

//nolint:unused
func (l *tracePipeLogger) Start() {
	channelEvents, channelErrors := l.Channel()

	go func() {
		for {
			select {
			case <-l.stop:
				for len(channelEvents) > 0 {
					l.handleEvent(<-channelEvents)
				}
				return
			case event := <-channelEvents:
				l.handleEvent(event)
			case err := <-channelErrors:
				log.Error(err)
			}
		}
	}()
}

//nolint:unused
func (l *tracePipeLogger) Stop() {
	time.Sleep(time.Millisecond * 200)

	l.stop <- struct{}{}
	l.Close()
}

//nolint:unused
func (tm *testModule) startTracing() (*tracePipeLogger, error) {
	tracePipe, err := NewTracePipe()
	if err != nil {
		return nil, err
	}

	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}

	logger := &tracePipeLogger{
		TracePipe:  tracePipe,
		stop:       make(chan struct{}),
		executable: filepath.Base(executable),
	}
	logger.Start()

	time.Sleep(time.Millisecond * 200)

	return logger, nil
}

func (tm *testModule) cleanup() {
	tm.st.Close()
	tm.module.Close()
}

func (tm *testModule) Close() {
	if !useReload {
		tm.cleanup()
	}
}

type simpleTest struct {
	root     string
	toRemove bool
}

func (t *simpleTest) Close() {
	if t.toRemove {
		os.RemoveAll(t.root)
	}
}

func (t *simpleTest) Root() string {
	return t.root
}

func (t *simpleTest) ProcessName() string {
	executable, _ := os.Executable()
	return path.Base(executable)
}

func (t *simpleTest) Path(filename ...string) (string, unsafe.Pointer, error) {
	components := []string{t.root}
	components = append(components, filename...)
	path := path.Join(components...)
	filenamePtr, err := syscall.BytePtrFromString(path)
	if err != nil {
		return "", nil, err
	}
	return path, unsafe.Pointer(filenamePtr), nil
}

func (t *simpleTest) load(macros []*rules.MacroDefinition, rules []*rules.RuleDefinition) (err error) {
	executeExpressionTemplate := func(expression string) (string, error) {
		buffer := new(bytes.Buffer)
		tmpl, err := template.New("").Parse(expression)
		if err != nil {
			return "", err
		}

		if err := tmpl.Execute(buffer, t); err != nil {
			return "", err
		}

		return buffer.String(), nil
	}

	for _, rule := range rules {
		if rule.Expression, err = executeExpressionTemplate(rule.Expression); err != nil {
			return err
		}
	}

	for _, macro := range macros {
		if macro.Expression, err = executeExpressionTemplate(macro.Expression); err != nil {
			return err
		}
	}

	return nil
}

var logInitilialized bool

func (t *simpleTest) swapLogLevel(logLevel seelog.LogLevel) (seelog.LogLevel, error) {
	if logger == nil {
		logFormat := "[%Date(2006-01-02 15:04:05.000)] [%LEVEL] %Func:%Line %Msg\n"

		var err error

		logger, err = seelog.LoggerFromWriterWithMinLevelAndFormat(os.Stdout, logLevel, logFormat)
		if err != nil {
			return 0, err
		}
	}
	log.SetupLogger(logger, logLevel.String())

	prevLevel, _ := seelog.LogLevelFromString(logLevelStr)
	logLevelStr = logLevel.String()
	return prevLevel, nil
}

func newSimpleTest(macros []*rules.MacroDefinition, rules []*rules.RuleDefinition, testDir string, logLevel seelog.LogLevel) (*simpleTest, error) {
	var err error

	t := &simpleTest{
		root: testDir,
	}

	if !logInitilialized {
		if _, err := t.swapLogLevel(logLevel); err != nil {
			return nil, err
		}

		logInitilialized = true
	}

	if testDir == "" {
		t.root, err = ioutil.TempDir("", "test-secagent-root")
		if err != nil {
			return nil, err
		}
		t.toRemove = true
		if err := os.Chmod(t.root, 0o711); err != nil {
			return nil, err
		}
	}

	if err := t.load(macros, rules); err != nil {
		return nil, err
	}

	return t, nil
}

// systemUmask caches the system umask between tests
var systemUmask int //nolint:unused

//nolint:deadcode,unused
func applyUmask(fileMode int) int {
	if systemUmask == 0 {
		// Get the system umask to compute the right access mode
		systemUmask = unix.Umask(0)
		// the previous line overrides the system umask, change it back
		_ = unix.Umask(systemUmask)
	}
	return fileMode &^ systemUmask
}

//nolint:deadcode,unused
func ifSyscallSupported(syscall string, test func(t *testing.T, syscallNB uintptr)) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()

		syscallNB, found := supportedSyscalls[syscall]
		if !found {
			t.Skipf("%s is not supported", syscall)
		}

		test(t, syscallNB)
	}
}

// waitForProbeEvent returns the first open event with the provided filename.
// WARNING: this function may yield a "fatal error: concurrent map writes" error if the ruleset of testModule does not
// contain a rule on "open.file.path"
//nolint:deadcode,unused
func waitForProbeEvent(test *testModule, action func() error, key string, value interface{}, eventType model.EventType) error {
	return test.GetProbeEvent(action, func(event *sprobe.Event) bool {
		if v, _ := event.GetFieldValue(key); v == value {
			return true
		}
		return false
	}, getEventTimeout, eventType)
}

//nolint:deadcode,unused
func waitForOpenProbeEvent(test *testModule, action func() error, filename string) error {
	return waitForProbeEvent(test, action, "open.file.path", filename, model.FileOpenEventType)
}

func TestEnv(t *testing.T) {
	if testEnvironment != "" && testEnvironment != HostEnvironment && testEnvironment != DockerEnvironment {
		t.Fatal("invalid environment")
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	retCode := m.Run()
	if useReload && testMod != nil {
		testMod.cleanup()
	}
	os.Exit(retCode)
}

func init() {
	os.Setenv("RUNTIME_SECURITY_TESTSUITE", "true")
	flag.StringVar(&testEnvironment, "env", HostEnvironment, "environment used to run the test suite: ex: host, docker")
	flag.BoolVar(&useReload, "reload", true, "reload rules instead of stopping/starting the agent for every test")
	flag.StringVar(&logLevelStr, "loglevel", seelog.WarnStr, "log level")
	flag.Var(&logPatterns, "logpattern", "List of log pattern")
	rand.Seed(time.Now().UnixNano())
}

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// randStringRunes returns a random string of the requested size
func randStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}
