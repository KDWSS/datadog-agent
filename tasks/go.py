"""
Golang related tasks go here
"""


import copy
import datetime
import glob
import os
import shutil
import textwrap
from pathlib import Path

from invoke import task
from invoke.exceptions import Exit

from .build_tags import get_default_build_tags
from .licenses import get_licenses_list
from .modules import DEFAULT_MODULES, generate_dummy_package
from .utils import get_build_flags

# List of modules to ignore when running lint
MODULE_ALLOWLIST = [
    # Windows
    "doflare.go",
    "iostats_pdh_windows.go",
    "iostats_wmi_windows.go",
    "pdh.go",
    "pdh_amd64.go",
    "pdh_386.go",
    "pdhformatter.go",
    "pdhhelper.go",
    "shutil.go",
    "tailer_windows.go",
    "winsec.go",
    "process_windows_toolhelp.go",
    "adapters.go",  # pkg/util/winutil/iphelper
    "routes.go",  # pkg/util/winutil/iphelper
    # All
    "agent.pb.go",
    "bbscache_test.go",
]

# List of paths to ignore in misspell's output
MISSPELL_IGNORED_TARGETS = [
    os.path.join("cmd", "agent", "dist", "checks", "prometheus_check"),
    os.path.join("cmd", "agent", "gui", "views", "private"),
    os.path.join("pkg", "collector", "corechecks", "system", "testfiles"),
    os.path.join("pkg", "ebpf", "testdata"),
    os.path.join("pkg", "network", "event_windows_test.go"),
]

# Packages that need go:generate
GO_GENERATE_TARGETS = ["./pkg/status", "./cmd/agent/gui"]


@task
def fmt(ctx, targets, fail_on_fmt=False):
    """
    Run go fmt on targets.

    Example invokation:
        inv fmt --targets=./pkg/collector/check,./pkg/aggregator
    """
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    result = ctx.run("gofmt -l -w -s " + " ".join(targets))
    if result.stdout:
        files = {x for x in result.stdout.split("\n") if x}
        print("Reformatted the following files: {}".format(','.join(files)))
        if fail_on_fmt:
            print("Code was not properly formatted, exiting...")
            raise Exit(code=1)
    print("gofmt found no issues")


@task
def lint(ctx, targets):
    """
    Run revive (the fork of golint) on targets. If targets are not specified,
    the value from `invoke.yaml` will be used.

    Example invokation:
        inv lint --targets=./pkg/collector/check,./pkg/aggregator
    """
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    # add the /... suffix to the targets
    targets_list = ["{}/...".format(t) for t in targets]
    cmd = "revive {}".format(' '.join(targets_list))
    if ctx.config.run.echo:
        # Hack so the command is printed if invoke -e is used
        # We use hide=True later to hide the output, but it also hides the command
        ctx.run(cmd, dry=True)
    result = ctx.run(cmd, hide=True)
    if result.stdout:
        files = set()
        skipped_files = set()
        for line in (out for out in result.stdout.split('\n') if out):
            fullname = line.split(":")[0]
            fname = os.path.basename(fullname)
            if fname in MODULE_ALLOWLIST:
                skipped_files.add(fullname)
                continue
            print(line)
            files.add(fullname)

        # add whitespace for readability
        print()

        if skipped_files:
            for skipped in skipped_files:
                print("Allowed errors in allowlisted file {}".format(skipped))

        # add whitespace for readability
        print()

        if files:
            print("Linting issues found in {} files.".format(len(files)))
            for f in files:
                print("Error in {}".format(f))
            raise Exit(code=1)

    print("revive found no issues")


@task
def vet(ctx, targets, rtloader_root=None, build_tags=None, arch="x64"):
    """
    Run go vet on targets.

    Example invokation:
        inv vet --targets=./pkg/collector/check,./pkg/aggregator
    """
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    # add the /... suffix to the targets
    args = ["{}/...".format(t) for t in targets]
    tags = build_tags or get_default_build_tags(build="test", arch=arch)
    tags.append("dovet")

    _, _, env = get_build_flags(ctx, rtloader_root=rtloader_root)
    env["CGO_ENABLED"] = "1"

    ctx.run("go vet -tags \"{}\" ".format(" ".join(tags)) + " ".join(args), env=env)
    # go vet exits with status 1 when it finds an issue, if we're here
    # everything went smooth
    print("go vet found no issues")


@task
def cyclo(ctx, targets, limit=15):
    """
    Run gocyclo on targets.
    Use the 'limit' parameter to change the maximum cyclic complexity.

    Example invokation:
        inv cyclo --targets=./pkg/collector/check,./pkg/aggregator
    """
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    ctx.run("gocyclo -over {} ".format(limit) + " ".join(targets))
    # gocyclo exits with status 1 when it finds an issue, if we're here
    # everything went smooth
    print("gocyclo found no issues")


@task
def golangci_lint(ctx, targets, rtloader_root=None, build_tags=None, arch="x64"):
    """
    Run golangci-lint on targets using .golangci.yml configuration.

    Example invocation:
        inv golangci-lint --targets=./pkg/collector/check,./pkg/aggregator
    """
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    tags = build_tags or get_default_build_tags(build="test", arch=arch)
    _, _, env = get_build_flags(ctx, rtloader_root=rtloader_root)
    # we split targets to avoid going over the memory limit from circleCI
    for target in targets:
        print("running golangci on {}".format(target))
        ctx.run(
            "golangci-lint run --timeout 10m0s --build-tags '{}' {}".format(" ".join(tags), "{}/...".format(target)),
            env=env,
        )

    # golangci exits with status 1 when it finds an issue, if we're here
    # everything went smooth
    print("golangci-lint found no issues")


@task
def ineffassign(ctx, targets):
    """
    Run ineffassign on targets.

    Example invokation:
        inv ineffassign --targets=./pkg/collector/check,./pkg/aggregator
    """
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    ctx.run("ineffassign " + " ".join(target + "/..." for target in targets))
    # ineffassign exits with status 1 when it finds an issue, if we're here
    # everything went smooth
    print("ineffassign found no issues")


@task
def staticcheck(ctx, targets, build_tags=None, arch="x64"):
    """
    Run staticcheck on targets.

    Example invokation:
        inv statickcheck --targets=./pkg/collector/check,./pkg/aggregator
    """
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    # staticcheck checks recursively only if path is in "path/..." format
    pkgs = [sub + "/..." for sub in targets]

    tags = copy.copy(build_tags or get_default_build_tags(build="test", arch=arch))
    # these two don't play well with static checking
    if "python" in tags:
        tags.remove("python")
    if "jmx" in tags:
        tags.remove("jmx")

    ctx.run("staticcheck -checks=SA1027 -tags=" + ",".join(tags) + " " + " ".join(pkgs))
    # staticcheck exits with status 1 when it finds an issue, if we're here
    # everything went smooth
    print("staticcheck found no issues")


@task
def misspell(ctx, targets):
    """
    Run misspell on targets.

    Example invokation:
        inv misspell --targets=./pkg/collector/check,./pkg/aggregator
    """
    if isinstance(targets, str):
        # when this function is called from the command line, targets are passed
        # as comma separated tokens in a string
        targets = targets.split(',')

    cmd = "misspell " + " ".join(targets)
    if ctx.config.run.echo:
        # Hack so the command is printed if invoke -e is used
        # We use hide=True later to hide the output, but it also hides the command
        ctx.run(cmd, dry=True)
    result = ctx.run(cmd, hide=True)
    legit_misspells = []
    for found_misspell in result.stdout.split("\n"):
        if len(found_misspell.strip()) > 0:
            if not any([ignored_target in found_misspell for ignored_target in MISSPELL_IGNORED_TARGETS]):
                legit_misspells.append(found_misspell)

    if len(legit_misspells) > 0:
        print("Misspell issues found:\n" + "\n".join(legit_misspells))
        raise Exit(code=2)
    else:
        print("misspell found no issues")


@task
def deps(ctx, verbose=False):
    """
    Setup Go dependencies
    """

    print("downloading dependencies")
    start = datetime.datetime.now()
    verbosity = ' -x' if verbose else ''
    ctx.run("go mod download{}".format(verbosity))
    dep_done = datetime.datetime.now()
    print("go mod download, elapsed: {}".format(dep_done - start))


@task
def deps_vendored(ctx, verbose=False):
    """
    Vendor Go dependencies
    """

    print("vendoring dependencies")
    start = datetime.datetime.now()
    verbosity = ' -v' if verbose else ''

    ctx.run("go mod vendor{}".format(verbosity))
    ctx.run("go mod tidy{}".format(verbosity))

    # "go mod vendor" doesn't copy files that aren't in a package: https://github.com/golang/go/issues/26366
    # This breaks when deps include other files that are needed (eg: .java files from gomobile): https://github.com/golang/go/issues/43736
    # For this reason, we need to use a 3rd party tool to copy these files.
    # We won't need this if/when we change to non-vendored modules
    ctx.run('modvendor -copy="**/*.c **/*.h **/*.proto **/*.java"{}'.format(verbosity))

    # If github.com/DataDog/datadog-agent gets vendored too - nuke it
    # This may happen because of the introduction of nested modules
    if os.path.exists('vendor/github.com/DataDog/datadog-agent'):
        print("Removing vendored github.com/DataDog/datadog-agent")
        shutil.rmtree('vendor/github.com/DataDog/datadog-agent')

    dep_done = datetime.datetime.now()
    print("go mod vendor, elapsed: {}".format(dep_done - start))


@task
def lint_licenses(ctx):
    """
    Checks that the LICENSE-3rdparty.csv file is up-to-date with contents of go.sum
    """
    print("Verify licenses")

    licenses = []
    file = 'LICENSE-3rdparty.csv'
    with open(file, 'r', encoding='utf-8') as f:
        next(f)
        for line in f:
            licenses.append(line.rstrip())

    new_licenses = get_licenses_list(ctx)

    removed_licenses = [ele for ele in new_licenses if ele not in licenses]
    for license in removed_licenses:
        print("+ {}".format(license))

    added_licenses = [ele for ele in licenses if ele not in new_licenses]
    for license in added_licenses:
        print("- {}".format(license))

    if len(removed_licenses) + len(added_licenses) > 0:
        raise Exit(
            message=textwrap.dedent(
                """\
                Licenses are not up-to-date.

                Please run 'inv generate-licenses' to update {}."""
            ).format(file),
            code=1,
        )

    print("Licenses are ok.")


@task
def generate_licenses(ctx, filename='LICENSE-3rdparty.csv', verbose=False):
    """
    Generates the LICENSE-3rdparty.csv file. Run this if `inv lint-licenses` fails.
    """
    new_licenses = get_licenses_list(ctx)

    # check that all licenses have a non-"UNKNOWN" copyright
    unknown_licenses = False
    for license in new_licenses:
        if license.endswith(',UNKNOWN'):
            unknown_licenses = True
            print("! {}".format(license))

    if unknown_licenses:
        raise Exit(
            message=textwrap.dedent(
                """\
                At least one dependency's copyright could not be determined.

                Consult the dependency's source, update `.copyright-overrides.yml` accordingly, and
                run `inv generate-licenses` to update {}."""
            ).format(filename),
            code=1,
        )

    with open(filename, 'w') as f:
        f.write("Component,Origin,License,Copyright\n")
        for license in new_licenses:
            if verbose:
                print(license)
            f.write('{}\n'.format(license))
    print("licenses files generated")


@task
def generate_protobuf(ctx):
    """
    Generates protobuf defintions in pkg/proto
    """
    base = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.abspath(os.path.join(base, ".."))
    proto_root = os.path.join(repo_root, "pkg", "proto")

    print("nuking old definitions at: {}".format(proto_root))
    file_list = glob.glob(os.path.join(proto_root, "pbgo", "*.go"))
    for file_path in file_list:
        try:
            os.remove(file_path)
        except OSError:
            print("Error while deleting file : ", file_path)

    with ctx.cd(repo_root):
        # protobuf defs
        print("generating protobuf code from: {}".format(proto_root))

        files = []
        for path in Path(os.path.join(proto_root, "datadog")).rglob('*.proto'):
            files.append(path.as_posix())

        ctx.run(
            "protoc -I{include_path} --go_out=plugins=grpc:{out_path} {targets}".format(
                include_path=proto_root,
                out_path=repo_root,
                targets=' '.join(files),
            )
        )
        # grpc-gateway logic
        ctx.run(
            "protoc -I{include_path} --grpc-gateway_out=logtostderr=true:{out_path} {targets}".format(
                include_path=proto_root,
                out_path=repo_root,
                targets=' '.join(files),
            )
        )
        # mockgen
        pbgo_dir = os.path.join(proto_root, "pbgo")
        mockgen_out = os.path.join(proto_root, "pbgo", "mocks")
        try:
            os.mkdir(mockgen_out)
        except FileExistsError:
            print("{} folder already exists".format(mockgen_out))

        ctx.run(
            "mockgen -source={in_path}/api.pb.go -destination={out_path}/api_mockgen.pb.go".format(
                in_path=pbgo_dir, out_path=mockgen_out
            )
        )

    # generate messagepack marshallers
    ctx.run(
        "msgp -file {in_path} -o={out_path}".format(
            in_path='pkg/proto/pbgo/config.pb.go',
            out_path='pkg/proto/pbgo/config_gen.go',
        )
    )


@task
def reset(ctx):
    """
    Clean everything and remove vendoring
    """
    # go clean
    print("Executing go clean")
    ctx.run("go clean")

    # remove the bin/ folder
    print("Remove agent binary folder")
    ctx.run("rm -rf ./bin/")

    # remove vendor folder
    print("Remove vendor folder")
    ctx.run("rm -rf ./vendor")


@task
def generate(ctx, mod="mod"):
    """
    Run go generate required package
    """
    ctx.run("go generate -mod={} ".format(mod) + " ".join(GO_GENERATE_TARGETS))
    print("go generate ran successfully")


@task
def check_mod_tidy(ctx, test_folder="testmodule"):
    errors_found = []
    for mod in DEFAULT_MODULES.values():
        with ctx.cd(mod.full_path()):
            ctx.run("go mod tidy")
            res = ctx.run("git diff-files --exit-code go.mod go.sum", warn=True)
            if res.exited is None or res.exited > 0:
                errors_found.append("go.mod or go.sum for {} module is out of sync".format(mod.import_path))

    generate_dummy_package(ctx, test_folder)
    with ctx.cd(test_folder):
        ctx.run("go mod tidy")
        res = ctx.run("go build main.go", warn=True)
        if res.exited is None or res.exited > 0:
            errors_found.append("could not build test module importing external modules")
        if os.path.isfile(os.path.join(ctx.cwd, "main")):
            os.remove(os.path.join(ctx.cwd, "main"))

    if errors_found:
        message = "\nErrors found:\n" + "\n".join("  - " + error for error in errors_found)
        message += "\n\nRun 'inv tidy-all' to fix 'out of sync' errors."
        raise Exit(message=message)


@task
def tidy_all(ctx):
    for mod in DEFAULT_MODULES.values():
        with ctx.cd(mod.full_path()):
            ctx.run("go mod tidy")
