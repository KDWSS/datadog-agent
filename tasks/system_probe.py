import contextlib
import glob
import json
import os
import shutil
import sys
import tempfile
from subprocess import CalledProcessError, check_output

from invoke import task
from invoke.exceptions import Exit

from .build_tags import get_default_build_tags
from .utils import REPO_PATH, bin_name, bundle_files, get_build_flags, get_version_numeric_only

BIN_DIR = os.path.join(".", "bin", "system-probe")
BIN_PATH = os.path.join(BIN_DIR, bin_name("system-probe", android=False))

BPF_TAG = "linux_bpf"
BUNDLE_TAG = "ebpf_bindata"
NPM_TAG = "npm"
GIMME_ENV_VARS = ['GOROOT', 'PATH']
DNF_TAG = "dnf"

CLANG_CMD = "clang {flags} -c '{c_file}' -o '{bc_file}'"
LLC_CMD = "llc -march=bpf -filetype=obj -o '{obj_file}' '{bc_file}'"

DATADOG_AGENT_EMBEDDED_PATH = '/opt/datadog-agent/embedded'

KITCHEN_DIR = os.getenv('DD_AGENT_TESTING_DIR') or os.path.normpath(os.path.join(os.getcwd(), "test", "kitchen"))
KITCHEN_ARTIFACT_DIR = os.path.join(KITCHEN_DIR, "site-cookbooks", "dd-system-probe-check", "files", "default", "tests")
TEST_PACKAGES_LIST = ["./pkg/ebpf/...", "./pkg/network/...", "./pkg/collector/corechecks/ebpf/..."]
TEST_PACKAGES = " ".join(TEST_PACKAGES_LIST)

is_windows = sys.platform == "win32"


@task
def build(
    ctx,
    race=False,
    incremental_build=False,
    major_version='7',
    python_runtimes='3',
    go_mod="mod",
    windows=is_windows,
    arch="x64",
    embedded_path=DATADOG_AGENT_EMBEDDED_PATH,
    compile_ebpf=True,
    nikos_embedded_path=None,
    bundle_ebpf=False,
    parallel_build=True,
):
    """
    Build the system_probe
    """

    # generate windows resources
    if windows:
        windres_target = "pe-x86-64"
        if arch == "x86":
            raise Exit(message="system probe not supported on x86")

        ver = get_version_numeric_only(ctx, major_version=major_version)
        maj_ver, min_ver, patch_ver = ver.split(".")
        resdir = os.path.join(".", "cmd", "system-probe", "windows_resources")

        ctx.run(
            "windmc --target {target_arch} -r {resdir} {resdir}/system-probe-msg.mc".format(
                resdir=resdir, target_arch=windres_target
            )
        )

        ctx.run(
            "windres "
            "--define MAJ_VER={maj_ver} "
            "--define MIN_VER={min_ver} "
            "--define PATCH_VER={patch_ver} "
            "-i cmd/system-probe/windows_resources/system-probe.rc "
            "--target {target_arch} "
            "-O coff "
            "-o cmd/system-probe/rsrc.syso".format(
                maj_ver=maj_ver, min_ver=min_ver, patch_ver=patch_ver, target_arch=windres_target
            )
        )
    elif compile_ebpf:
        # Only build ebpf files on unix
        build_object_files(ctx, parallel_build=parallel_build)

    generate_cgo_types(ctx, windows=windows)
    ldflags, gcflags, env = get_build_flags(
        ctx,
        major_version=major_version,
        python_runtimes=python_runtimes,
        embedded_path=embedded_path,
        nikos_embedded_path=nikos_embedded_path,
    )

    build_tags = get_default_build_tags(build="system-probe", arch=arch)
    if bundle_ebpf:
        build_tags.append(BUNDLE_TAG)
    if nikos_embedded_path:
        build_tags.append(DNF_TAG)

    cmd = 'go build -mod={go_mod} {race_opt} {build_type} -tags "{go_build_tags}" '
    cmd += '-o {agent_bin} -gcflags="{gcflags}" -ldflags="{ldflags}" {REPO_PATH}/cmd/system-probe'

    args = {
        "go_mod": go_mod,
        "race_opt": "-race" if race else "",
        "build_type": "" if incremental_build else "-a",
        "go_build_tags": " ".join(build_tags),
        "agent_bin": BIN_PATH,
        "gcflags": gcflags,
        "ldflags": ldflags,
        "REPO_PATH": REPO_PATH,
    }

    ctx.run(cmd.format(**args), env=env)


@task
def test(
    ctx,
    packages=TEST_PACKAGES,
    skip_object_files=False,
    bundle_ebpf=False,
    output_path=None,
    runtime_compiled=False,
    skip_linters=False,
    run=None,
    windows=is_windows,
    parallel_build=True,
):
    """
    Run tests on eBPF parts
    If skip_object_files is set to True, this won't rebuild object files
    If output_path is set, we run `go test` with the flags `-c -o output_path`, which *compiles* the test suite
    into a single binary. This artifact is meant to be used in conjunction with kitchen tests.
    """
    if os.getenv("GOPATH") is None:
        raise Exit(
            code=1,
            message="GOPATH is not set, if you are running tests with sudo, you may need to use the -E option to "
            "preserve your environment",
        )

    if not skip_linters and not windows:
        clang_format(ctx)
        clang_tidy(ctx)

    if not skip_object_files and not windows:
        build_object_files(ctx, parallel_build=parallel_build)

    build_tags = [NPM_TAG]
    if not windows:
        build_tags.append(BPF_TAG)
        if bundle_ebpf:
            build_tags.append(BUNDLE_TAG)

    args = {
        "build_tags": ",".join(build_tags),
        "output_params": "-c -o " + output_path if output_path else "",
        "pkgs": packages,
        "run": "-run " + run if run else "",
    }

    _, _, env = get_build_flags(ctx)
    env['DD_SYSTEM_PROBE_BPF_DIR'] = os.path.normpath(os.path.join(os.getcwd(), "pkg", "ebpf", "bytecode", "build"))
    if runtime_compiled:
        env['DD_TESTS_RUNTIME_COMPILED'] = "1"

    cmd = 'go test -mod=mod -v -tags "{build_tags}" {output_params} {pkgs} {run}'
    if not windows and not output_path and not is_root():
        cmd = 'sudo -E ' + cmd

    ctx.run(cmd.format(**args), env=env)


@task
def kitchen_prepare(ctx, windows=is_windows):
    """
    Compile test suite for kitchen
    """

    # Clean up previous build
    if os.path.exists(KITCHEN_ARTIFACT_DIR):
        shutil.rmtree(KITCHEN_ARTIFACT_DIR)

    build_tags = [NPM_TAG]
    if not windows:
        build_tags.append(BPF_TAG)

    # Retrieve a list of all packages we want to test
    # This handles the elipsis notation (eg. ./pkg/ebpf/...)
    target_packages = []
    for pkg in TEST_PACKAGES_LIST:
        target_packages += (
            check_output(
                'go list -f "{{{{ .Dir }}}}" -mod=mod -tags "{tags}" {pkg}'.format(tags=",".join(build_tags), pkg=pkg),
                shell=True,
            )
            .decode('utf-8')
            .strip()
            .split("\n")
        )

    # This will compile one 'testsuite' file per package by running `go test -c -o output_path`.
    # These artifacts will be "vendored" inside a chef recipe like the following:
    # test/kitchen/site-cookbooks/dd-system-probe-check/files/default/tests/pkg/network/testsuite
    # test/kitchen/site-cookbooks/dd-system-probe-check/files/default/tests/pkg/network/netlink/testsuite
    # test/kitchen/site-cookbooks/dd-system-probe-check/files/default/tests/pkg/ebpf/testsuite
    # test/kitchen/site-cookbooks/dd-system-probe-check/files/default/tests/pkg/ebpf/bytecode/testsuite
    for i, pkg in enumerate(target_packages):
        relative_path = os.path.relpath(pkg)
        target_path = os.path.join(KITCHEN_ARTIFACT_DIR, relative_path)

        test(
            ctx,
            packages=pkg,
            skip_object_files=(i != 0),
            skip_linters=True,
            bundle_ebpf=False,
            output_path=os.path.join(target_path, "testsuite"),
        )

        # copy ancillary data, if applicable
        for extra in ["testdata", "build"]:
            extra_path = os.path.join(pkg, extra)
            if os.path.isdir(extra_path):
                shutil.copytree(extra_path, os.path.join(target_path, extra))


@task
def kitchen_test(ctx, target=None, arch="x86_64"):
    """
    Run tests (locally) using chef kitchen against an array of different platforms.
    * Make sure to run `inv -e system-probe.kitchen-prepare` using the agent-development VM;
    * Then we recommend to run `inv -e system-probe.kitchen-test` directly from your (macOS) machine;
    """

    # Retrieve a list of all available vagrant images
    images = {}
    with open(os.path.join(KITCHEN_DIR, "platforms.json"), 'r') as f:
        for platform, by_provider in json.load(f).items():
            if "vagrant" in by_provider:
                for image in by_provider["vagrant"][arch]:
                    images[image] = platform

    if not (target in images):
        print(
            "please run inv -e system-probe.kitchen-test --target <IMAGE>, where <IMAGE> is one of the following:\n%s"
            % (list(images.keys()))
        )
        raise Exit(code=1)

    with ctx.cd(KITCHEN_DIR):
        ctx.run(
            "inv kitchen.genconfig --platform {platform} --osversions {target} --provider vagrant --testfiles system-probe-test".format(
                target=target, platform=images[target]
            ),
            env={"KITCHEN_VAGRANT_PROVIDER": "virtualbox"},
        )
        ctx.run("kitchen test")


@task
def nettop(ctx, incremental_build=False, go_mod="mod", parallel_build=True):
    """
    Build and run the `nettop` utility for testing
    """
    build_object_files(ctx, parallel_build=parallel_build)

    cmd = 'go build -mod={go_mod} {build_type} -tags {tags} -o {bin_path} {path}'
    bin_path = os.path.join(BIN_DIR, "nettop")
    # Build
    ctx.run(
        cmd.format(
            path=os.path.join(REPO_PATH, "pkg", "network", "nettop"),
            bin_path=bin_path,
            go_mod=go_mod,
            build_type="" if incremental_build else "-a",
            tags=BPF_TAG,
        )
    )

    # Run
    if not is_root():
        ctx.sudo(bin_path)
    else:
        ctx.run(bin_path)


@task
def clang_format(ctx, targets=None, fix=False, fail_on_issue=False):
    """
    Format C code using clang-format
    """
    ctx.run("which clang-format")
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    if not targets:
        targets = get_ebpf_targets()

    # remove externally maintained files
    ignored_files = ["pkg/ebpf/c/bpf_helpers.h", "pkg/ebpf/c/bpf_endian.h", "pkg/ebpf/compiler/clang-stdarg.h"]
    for f in ignored_files:
        if f in targets:
            targets.remove(f)

    fmt_cmd = "clang-format -i --style=file --fallback-style=none"
    if not fix:
        fmt_cmd = fmt_cmd + " --dry-run"
    if fail_on_issue:
        fmt_cmd = fmt_cmd + " --Werror"

    ctx.run("{cmd} {files}".format(cmd=fmt_cmd, files=" ".join(targets)))


@task
def clang_tidy(ctx, fix=False, fail_on_issue=False):
    """
    Lint C code using clang-tidy
    """

    print("checking for clang-tidy executable...")
    ctx.run("which clang-tidy")

    build_flags = get_ebpf_build_flags()
    build_flags.append("-DDEBUG=1")

    bpf_dir = os.path.join(".", "pkg", "ebpf")
    base_files = glob.glob(bpf_dir + "/c/**/*.c")

    network_bpf_dir = os.path.join(".", "pkg", "network", "ebpf")
    network_c_dir = os.path.join(network_bpf_dir, "c")
    network_files = list(base_files)
    network_files.extend(glob.glob(network_c_dir + "/**/*.c"))
    network_flags = list(build_flags)
    network_flags.append("-I{}".format(network_c_dir))
    network_flags.append("-I{}".format(os.path.join(network_c_dir, "prebuilt")))
    network_flags.append("-I{}".format(os.path.join(network_c_dir, "runtime")))
    run_tidy(ctx, files=network_files, build_flags=network_flags, fix=fix, fail_on_issue=fail_on_issue)

    security_agent_c_dir = os.path.join(".", "pkg", "security", "ebpf", "c")
    security_files = list(base_files)
    security_files.extend(glob.glob(security_agent_c_dir + "/**/*.c"))
    security_flags = list(build_flags)
    security_flags.append("-I{}".format(security_agent_c_dir))
    security_flags.append("-DUSE_SYSCALL_WRAPPER=0")
    run_tidy(ctx, files=security_files, build_flags=security_flags, fix=fix, fail_on_issue=fail_on_issue)


def run_tidy(ctx, files, build_flags, fix=False, fail_on_issue=False):
    flags = ["--quiet"]
    if fix:
        flags.append("--fix")
    if fail_on_issue:
        flags.append("--warnings-as-errors='*'")

    ctx.run(
        "clang-tidy {flags} {files} -- {build_flags}".format(
            flags=" ".join(flags), build_flags=" ".join(build_flags), files=" ".join(files)
        )
    )


@task
def object_files(ctx, parallel_build=True):
    """object_files builds the eBPF object files"""
    build_object_files(ctx, parallel_build=parallel_build)


def get_ebpf_c_files():
    files = glob.glob("pkg/ebpf/c/**/*.c")
    files.extend(glob.glob("pkg/network/ebpf/c/**/*.c"))
    files.extend(glob.glob("pkg/security/ebpf/c/**/*.c"))
    files.extend(glob.glob("pkg/collector/corechecks/ebpf/c/**/*.c"))
    return files


def get_ebpf_targets():
    files = glob.glob("pkg/ebpf/c/*.[c,h]")
    files.extend(glob.glob("pkg/network/ebpf/c/*.[c,h]"))
    files.extend(glob.glob("pkg/security/ebpf/c/*.[c,h]"))
    return files


def get_linux_header_dirs():
    os_info = os.uname()
    centos_headers_dir = "/usr/src/kernels"
    debian_headers_dir = "/usr/src"
    linux_headers = []
    if os.path.isdir(centos_headers_dir):
        for d in os.listdir(centos_headers_dir):
            if os_info.release in d:
                linux_headers.append(os.path.join(centos_headers_dir, d))
    else:
        for d in os.listdir(debian_headers_dir):
            if d.startswith("linux-") and os_info.release in d:
                linux_headers.append(os.path.join(debian_headers_dir, d))

    # fallback to non-filtered version for Docker where `uname -r` is not correct
    if len(linux_headers) == 0:
        if os.path.isdir(centos_headers_dir):
            linux_headers = [os.path.join(centos_headers_dir, d) for d in os.listdir(centos_headers_dir)]
        else:
            linux_headers = [
                os.path.join(debian_headers_dir, d) for d in os.listdir(debian_headers_dir) if d.startswith("linux-")
            ]

    # fallback to the running kernel/build headers via /lib/modules/$(uname -r)/build/
    if len(linux_headers) == 0:
        uname_r = check_output('''uname -r''', shell=True).decode('utf-8').strip()
        build_dir = "/lib/modules/{}/build".format(uname_r)
        if os.path.isdir(build_dir):
            linux_headers = [build_dir]

    # Mapping used by the kernel, from https://elixir.bootlin.com/linux/latest/source/scripts/subarch.include
    arch = (
        check_output(
            '''uname -m | sed -e s/i.86/x86/ -e s/x86_64/x86/ \
                    -e s/sun4u/sparc64/ \
                    -e s/arm.*/arm/ -e s/sa110/arm/ \
                    -e s/s390x/s390/ -e s/parisc64/parisc/ \
                    -e s/ppc.*/powerpc/ -e s/mips.*/mips/ \
                    -e s/sh[234].*/sh/ -e s/aarch64.*/arm64/ \
                    -e s/riscv.*/riscv/''',
            shell=True,
        )
        .decode('utf-8')
        .strip()
    )

    subdirs = [
        "include",
        "include/uapi",
        "include/generated/uapi",
        "arch/{}/include".format(arch),
        "arch/{}/include/uapi".format(arch),
        "arch/{}/include/generated".format(arch),
    ]

    dirs = []
    for d in linux_headers:
        for s in subdirs:
            dirs.extend([os.path.join(d, s)])

    return dirs


def get_ebpf_build_flags():
    bpf_dir = os.path.join(".", "pkg", "ebpf")
    c_dir = os.path.join(bpf_dir, "c")

    flags = [
        '-D__KERNEL__',
        '-DCONFIG_64BIT',
        '-D__BPF_TRACING__',
        '-DKBUILD_MODNAME=\\"ddsysprobe\\"',
        '-Wno-unused-value',
        '-Wno-pointer-sign',
        '-Wno-compare-distinct-pointer-types',
        '-Wunused',
        '-Wall',
        '-Werror',
        "-include {}".format(os.path.join(c_dir, "asm_goto_workaround.h")),
        '-O2',
        '-emit-llvm',
        # Some linux distributions enable stack protector by default which is not available on eBPF
        '-fno-stack-protector',
        '-fno-color-diagnostics',
        '-fno-unwind-tables',
        '-fno-asynchronous-unwind-tables',
        '-fno-jump-tables',
        "-I{}".format(c_dir),
    ]

    header_dirs = get_linux_header_dirs()
    for d in header_dirs:
        flags.extend(["-isystem", d])

    return flags


def build_network_ebpf_compile_file(ctx, parallel_build, build_dir, p, debug, network_prebuilt_dir, network_flags):
    src_file = os.path.join(network_prebuilt_dir, "{}.c".format(p))
    if not debug:
        bc_file = os.path.join(build_dir, "{}.bc".format(p))
        return ctx.run(
            CLANG_CMD.format(flags=" ".join(network_flags), bc_file=bc_file, c_file=src_file),
            asynchronous=parallel_build,
        )
    else:
        debug_bc_file = os.path.join(build_dir, "{}-debug.bc".format(p))
        return ctx.run(
            CLANG_CMD.format(flags=" ".join(network_flags + ["-DDEBUG=1"]), bc_file=debug_bc_file, c_file=src_file),
            asynchronous=parallel_build,
        )


def build_network_ebpf_link_file(ctx, parallel_build, build_dir, p, debug, network_flags):
    if not debug:
        bc_file = os.path.join(build_dir, "{}.bc".format(p))
        obj_file = os.path.join(build_dir, "{}.o".format(p))
        return ctx.run(
            LLC_CMD.format(flags=" ".join(network_flags), bc_file=bc_file, obj_file=obj_file),
            asynchronous=parallel_build,
        )
    else:
        debug_bc_file = os.path.join(build_dir, "{}-debug.bc".format(p))
        debug_obj_file = os.path.join(build_dir, "{}-debug.o".format(p))
        return ctx.run(
            LLC_CMD.format(flags=" ".join(network_flags), bc_file=debug_bc_file, obj_file=debug_obj_file),
            asynchronous=parallel_build,
        )


def build_network_ebpf_files(ctx, build_dir, parallel_build=True):
    network_bpf_dir = os.path.join(".", "pkg", "network", "ebpf")
    network_c_dir = os.path.join(network_bpf_dir, "c")
    network_prebuilt_dir = os.path.join(network_c_dir, "prebuilt")

    compiled_programs = ["dns", "http", "offset-guess", "tracer"]

    network_flags = get_ebpf_build_flags()
    network_flags.append("-I{}".format(network_c_dir))

    flavor = []
    for prog in compiled_programs:
        for debug in [False, True]:
            flavor.append((prog, debug))

    promises = []
    for p, debug in flavor:
        promises.append(
            build_network_ebpf_compile_file(
                ctx, parallel_build, build_dir, p, debug, network_prebuilt_dir, network_flags
            )
        )
        if not parallel_build:
            build_network_ebpf_link_file(ctx, parallel_build, build_dir, p, debug, network_flags)

    if not parallel_build:
        return

    promises_link = []
    for i, promise in enumerate(promises):
        promise.join()
        (p, debug) = flavor[i]
        promises_link.append(build_network_ebpf_link_file(ctx, parallel_build, build_dir, p, debug, network_flags))

    for promise in promises_link:
        promise.join()


def build_security_ebpf_files(ctx, build_dir, parallel_build=True):
    security_agent_c_dir = os.path.join(".", "pkg", "security", "ebpf", "c")
    security_agent_prebuilt_dir = os.path.join(security_agent_c_dir, "prebuilt")
    security_c_file = os.path.join(security_agent_prebuilt_dir, "probe.c")
    security_bc_file = os.path.join(build_dir, "runtime-security.bc")
    security_agent_obj_file = os.path.join(build_dir, "runtime-security.o")

    security_flags = get_ebpf_build_flags()
    security_flags.append("-I{}".format(security_agent_c_dir))

    # compile
    promises = []
    promises.append(
        ctx.run(
            CLANG_CMD.format(
                flags=" ".join(security_flags + ["-DUSE_SYSCALL_WRAPPER=0"]),
                c_file=security_c_file,
                bc_file=security_bc_file,
            ),
            asynchronous=parallel_build,
        )
    )
    security_agent_syscall_wrapper_bc_file = os.path.join(build_dir, "runtime-security-syscall-wrapper.bc")
    promises.append(
        ctx.run(
            CLANG_CMD.format(
                flags=" ".join(security_flags + ["-DUSE_SYSCALL_WRAPPER=1"]),
                c_file=security_c_file,
                bc_file=security_agent_syscall_wrapper_bc_file,
            ),
            asynchronous=parallel_build,
        )
    )

    if parallel_build:
        for p in promises:
            p.join()

    # link
    promises = []
    promises.append(
        ctx.run(
            LLC_CMD.format(flags=" ".join(security_flags), bc_file=security_bc_file, obj_file=security_agent_obj_file),
            asynchronous=parallel_build,
        )
    )

    security_agent_syscall_wrapper_obj_file = os.path.join(build_dir, "runtime-security-syscall-wrapper.o")
    promises.append(
        ctx.run(
            LLC_CMD.format(
                flags=" ".join(security_flags),
                bc_file=security_agent_syscall_wrapper_bc_file,
                obj_file=security_agent_syscall_wrapper_obj_file,
            ),
            asynchronous=parallel_build,
        )
    )

    if parallel_build:
        for p in promises:
            p.join()

    return [security_agent_obj_file, security_agent_syscall_wrapper_obj_file]


def build_object_files(ctx, parallel_build):
    """build_object_files builds only the eBPF object"""

    # if clang is missing, subsequent calls to ctx.run("clang ...") will fail silently
    print("checking for clang executable...")
    ctx.run("which clang")

    bpf_dir = os.path.join(".", "pkg", "ebpf")
    build_dir = os.path.join(bpf_dir, "bytecode", "build")
    build_runtime_dir = os.path.join(build_dir, "runtime")

    ctx.run("mkdir -p {build_dir}".format(build_dir=build_dir))
    ctx.run("mkdir -p {build_runtime_dir}".format(build_runtime_dir=build_runtime_dir))

    bindata_files = []
    build_network_ebpf_files(ctx, build_dir=build_dir, parallel_build=parallel_build)
    bindata_files.extend(build_security_ebpf_files(ctx, build_dir=build_dir, parallel_build=parallel_build))

    generate_runtime_files(ctx)

    go_dir = os.path.join(bpf_dir, "bytecode", "bindata")
    bundle_files(ctx, bindata_files, "pkg/.*/", go_dir, "bindata", BUNDLE_TAG)


@task
def generate_runtime_files(ctx):
    runtime_compiler_files = [
        "./pkg/collector/corechecks/ebpf/probe/oom_kill.go",
        "./pkg/collector/corechecks/ebpf/probe/tcp_queue_length.go",
        "./pkg/network/tracer/compile.go",
        "./pkg/network/tracer/connection/kprobe/compile.go",
        "./pkg/security/probe/compile.go",
    ]
    for f in runtime_compiler_files:
        ctx.run("go generate -mod=mod -tags {tags} {file}".format(file=f, tags=BPF_TAG))


def replace_cgo_tag_absolute_path(file_path, absolute_path, relative_path):
    # read
    f = open(file_path)
    lines = []
    for line in f:
        if line.startswith("// cgo -godefs"):
            lines.append(line.replace(absolute_path, relative_path))
        else:
            lines.append(line)
    f.close()

    # write
    f = open(file_path, "w")
    res = "".join(lines)
    f.write(res)
    f.close()


@task
def generate_cgo_types(ctx, windows=is_windows, replace_absolutes=True):
    if windows:
        platform = "windows"
        def_files = ["./pkg/network/driver/types.go"]
    else:
        platform = "linux"
        def_files = [
            "./pkg/network/ebpf/offsetguess_types.go",
            "./pkg/network/ebpf/conntrack_types.go",
            "./pkg/network/ebpf/tuple_types.go",
            "./pkg/network/ebpf/kprobe_types.go",
        ]

    for f in def_files:
        fdir, file = os.path.split(f)
        absolute_input_file = os.path.abspath(f)
        base, _ = os.path.splitext(file)
        with ctx.cd(fdir):
            output_file = "{base}_{platform}.go".format(base=base, platform=platform)
            ctx.run(
                "go tool cgo -godefs -- -fsigned-char {file} > {output_file}".format(file=file, output_file=output_file)
            )
            ctx.run("gofmt -w -s {output_file}".format(output_file=output_file))
            if replace_absolutes:
                # replace absolute path with relative ones in generated file
                replace_cgo_tag_absolute_path(os.path.join(fdir, output_file), absolute_input_file, file)


def is_root():
    return os.getuid() == 0


def should_docker_use_sudo(_):
    # We are already root
    if is_root():
        return False

    with open(os.devnull, 'w') as FNULL:
        try:
            check_output(['docker', 'info'], stderr=FNULL)
        except CalledProcessError:
            return True

    return False


@contextlib.contextmanager
def tempdir():
    """
    Helper to create a temp directory and clean it
    """
    dirpath = tempfile.mkdtemp()
    try:
        yield dirpath
    finally:
        shutil.rmtree(dirpath)
