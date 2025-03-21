"""
Release helper tasks
"""

import hashlib
import json
import re
import sys
from collections import OrderedDict
from datetime import date
from time import sleep

from invoke import Failure, task
from invoke.exceptions import Exit

from tasks.libs.common.color import color_message
from tasks.libs.common.github_api import GithubAPI, get_github_token
from tasks.libs.common.gitlab import Gitlab, get_gitlab_token
from tasks.pipeline import run
from tasks.utils import DEFAULT_BRANCH, get_version, nightly_entry_for, release_entry_for

from .libs.common.user_interactions import yes_no_question
from .libs.version import Version
from .modules import DEFAULT_MODULES

# Generic version regex. Aims to match:
# - X.Y.Z
# - X.Y.Z-rc.t
# - X.Y.Z-devel
# - vX.Y(.Z) (security-agent-policies repo)
VERSION_RE = re.compile(r'(v)?(\d+)[.](\d+)([.](\d+))?(-devel)?(-rc\.(\d+))?')

REPOSITORY_NAME = "DataDog/datadog-agent"


@task
def add_prelude(ctx, version):
    res = ctx.run("reno new prelude-release-{0}".format(version))
    new_releasenote = res.stdout.split(' ')[-1].strip()  # get the new releasenote file path

    with open(new_releasenote, "w") as f:
        f.write(
            """prelude:
    |
    Release on: {1}

    - Please refer to the `{0} tag on integrations-core <https://github.com/DataDog/integrations-core/blob/master/AGENT_CHANGELOG.md#datadog-agent-version-{2}>`_ for the list of changes on the Core Checks\n""".format(
                version, date.today(), version.replace('.', '')
            )
        )

    ctx.run("git add {}".format(new_releasenote))
    print("\nCommit this with:")
    print("git commit -m \"Add prelude for {} release\"".format(version))


@task
def add_dca_prelude(ctx, version, agent7_version, agent6_version=""):
    """
    Release of the Cluster Agent should be pinned to a version of the Agent.
    """
    res = ctx.run("reno --rel-notes-dir releasenotes-dca new prelude-release-{0}".format(version))
    new_releasenote = res.stdout.split(' ')[-1].strip()  # get the new releasenote file path

    if agent6_version != "":
        agent6_version = "--{}".format(
            agent6_version.replace('.', '')
        )  # generate the right hyperlink to the agent's changelog.

    with open(new_releasenote, "w") as f:
        f.write(
            """prelude:
    |
    Released on: {1}
    Pinned to datadog-agent v{0}: `CHANGELOG <https://github.com/{5}/blob/{4}/CHANGELOG.rst#{2}{3}>`_.""".format(
                agent7_version,
                date.today(),
                agent7_version.replace('.', ''),
                agent6_version,
                DEFAULT_BRANCH,
                REPOSITORY_NAME,
            )
        )

    ctx.run("git add {}".format(new_releasenote))
    print("\nCommit this with:")
    print("git commit -m \"Add prelude for {} release\"".format(version))


@task
def add_installscript_prelude(ctx, version):
    res = ctx.run("reno --rel-notes-dir releasenotes-installscript new prelude-release-{0}".format(version))
    new_releasenote = res.stdout.split(' ')[-1].strip()  # get the new releasenote file path

    with open(new_releasenote, "w") as f:
        f.write(
            """prelude:
    |
    Released on: {0}""".format(
                date.today()
            )
        )

    ctx.run("git add {}".format(new_releasenote))
    print("\nCommit this with:")
    print("git commit -m \"Add prelude for {} release\"".format(version))


@task
def update_dca_changelog(ctx, new_version, agent_version):
    """
    Quick task to generate the new CHANGELOG-DCA using reno when releasing a minor
    version (linux/macOS only).
    """
    new_version_int = list(map(int, new_version.split(".")))

    if len(new_version_int) != 3:
        print("Error: invalid version: {}".format(new_version_int))
        raise Exit(1)

    agent_version_int = list(map(int, agent_version.split(".")))

    if len(agent_version_int) != 3:
        print("Error: invalid version: {}".format(agent_version_int))
        raise Exit(1)

    # let's avoid losing uncommitted change with 'git reset --hard'
    try:
        ctx.run("git diff --exit-code HEAD", hide="both")
    except Failure:
        print("Error: You have uncommitted changes, please commit or stash before using update-dca-changelog")
        return

    # make sure we are up to date
    ctx.run("git fetch")

    # let's check that the tag for the new version is present (needed by reno)
    try:
        ctx.run("git tag --list | grep dca-{}".format(new_version))
    except Failure:
        print("Missing 'dca-{}' git tag: mandatory to use 'reno'".format(new_version))
        raise

    # Cluster agent minor releases are in sync with the agent's, bugfixes are not necessarily.
    # We rely on the agent's devel tag to enforce the sync between both releases.
    branching_point_agent = "{}.{}.0-devel".format(agent_version_int[0], agent_version_int[1])
    previous_minor_branchoff = "dca-{}.{}.X".format(new_version_int[0], new_version_int[1] - 1)
    log_result = ctx.run(
        "git log {}...remotes/origin/{} --name-only --oneline | \
            grep releasenotes-dca/notes/ || true".format(
            branching_point_agent, previous_minor_branchoff
        )
    )
    log_result = log_result.stdout.replace('\n', ' ').strip()

    # Do not include release notes that were added in the previous minor release branch (previous_minor_branchoff)
    # and the branch-off points for the current release (pined by the agent's devel tag)
    if len(log_result) > 0:
        ctx.run("git rm --ignore-unmatch {}".format(log_result))

    current_branchoff = "dca-{}.{}.X".format(new_version_int[0], new_version_int[1])
    # generate the new changelog. Specifying branch in case this is run outside the release branch that contains the tag.
    ctx.run(
        "reno --rel-notes-dir releasenotes-dca report \
            --ignore-cache \
            --branch {} \
            --version dca-{} \
            --no-show-source > /tmp/new_changelog-dca.rst".format(
            current_branchoff, new_version
        )
    )

    # reseting git
    ctx.run("git reset --hard HEAD")

    # mac's `sed` has a different syntax for the "-i" paramter
    sed_i_arg = "-i"
    if sys.platform == 'darwin':
        sed_i_arg = "-i ''"
    # remove the old header from the existing changelog
    ctx.run("sed {0} -e '1,4d' CHANGELOG-DCA.rst".format(sed_i_arg))

    if sys.platform != 'darwin':
        # sed on darwin doesn't support `-z`. On mac, you will need to manually update the following.
        ctx.run(
            "sed -z {0} -e 's/dca-{1}\\n===={2}/{1}\\n{2}/' /tmp/new_changelog-dca.rst".format(
                sed_i_arg, new_version, '=' * len(new_version)
            )
        )

    # merging to CHANGELOG.rst
    ctx.run("cat CHANGELOG-DCA.rst >> /tmp/new_changelog-dca.rst && mv /tmp/new_changelog-dca.rst CHANGELOG-DCA.rst")

    # commit new CHANGELOG
    ctx.run("git add CHANGELOG-DCA.rst")

    print("\nCommit this with:")
    print("git commit -m \"[DCA] Update CHANGELOG for {}\"".format(new_version))


@task
def update_changelog(ctx, new_version):
    """
    Quick task to generate the new CHANGELOG using reno when releasing a minor
    version (linux/macOS only).
    """
    new_version_int = list(map(int, new_version.split(".")))

    if len(new_version_int) != 3:
        print("Error: invalid version: {}".format(new_version_int))
        raise Exit(1)

    # let's avoid losing uncommitted change with 'git reset --hard'
    try:
        ctx.run("git diff --exit-code HEAD", hide="both")
    except Failure:
        print("Error: You have uncommitted change, please commit or stash before using update_changelog")
        return

    # make sure we are up to date
    ctx.run("git fetch")

    # let's check that the tag for the new version is present (needed by reno)
    try:
        ctx.run("git tag --list | grep {}".format(new_version))
    except Failure:
        print("Missing '{}' git tag: mandatory to use 'reno'".format(new_version))
        raise

    # removing releasenotes from bugfix on the old minor.
    branching_point = "{}.{}.0-devel".format(new_version_int[0], new_version_int[1])
    previous_minor = "{}.{}".format(new_version_int[0], new_version_int[1] - 1)
    if previous_minor == "7.15":
        previous_minor = "6.15"  # 7.15 is the first release in the 7.x series
    log_result = ctx.run(
        "git log {}...remotes/origin/{}.x --name-only --oneline | \
            grep releasenotes/notes/ || true".format(
            branching_point, previous_minor
        )
    )
    log_result = log_result.stdout.replace('\n', ' ').strip()
    if len(log_result) > 0:
        ctx.run("git rm --ignore-unmatch {}".format(log_result))

    # generate the new changelog
    ctx.run(
        "reno report \
            --ignore-cache \
            --earliest-version {} \
            --version {} \
            --no-show-source > /tmp/new_changelog.rst".format(
            branching_point, new_version
        )
    )

    # reseting git
    ctx.run("git reset --hard HEAD")

    # mac's `sed` has a different syntax for the "-i" paramter
    sed_i_arg = "-i"
    if sys.platform == 'darwin':
        sed_i_arg = "-i ''"
    # check whether there is a v6 tag on the same v7 tag, if so add the v6 tag to the release title
    v6_tag = ""
    if new_version_int[0] == 7:
        v6_tag = _find_v6_tag(ctx, new_version)
        if v6_tag:
            ctx.run("sed {0} -E 's#^{1}#{1} / {2}#' /tmp/new_changelog.rst".format(sed_i_arg, new_version, v6_tag))
    # remove the old header from the existing changelog
    ctx.run("sed {0} -e '1,4d' CHANGELOG.rst".format(sed_i_arg))

    # merging to CHANGELOG.rst
    ctx.run("cat CHANGELOG.rst >> /tmp/new_changelog.rst && mv /tmp/new_changelog.rst CHANGELOG.rst")

    # commit new CHANGELOG
    ctx.run("git add CHANGELOG.rst")

    print("\nCommit this with:")
    print("git commit -m \"[DCA] Update CHANGELOG for {}\"".format(new_version))


@task
def update_installscript_changelog(ctx, new_version):
    """
    Quick task to generate the new CHANGELOG-INSTALLSCRIPT using reno when releasing a minor
    version (linux/macOS only).
    """
    new_version_int = list(map(int, new_version.split(".")))

    if len(new_version_int) != 3:
        print("Error: invalid version: {}".format(new_version_int))
        raise Exit(1)

    # let's avoid losing uncommitted change with 'git reset --hard'
    try:
        ctx.run("git diff --exit-code HEAD", hide="both")
    except Failure:
        print("Error: You have uncommitted changes, please commit or stash before using update-installscript-changelog")
        return

    # make sure we are up to date
    ctx.run("git fetch")

    # let's check that the tag for the new version is present (needed by reno)
    try:
        ctx.run("git tag --list | grep installscript-{}".format(new_version))
    except Failure:
        print("Missing 'installscript-{}' git tag: mandatory to use 'reno'".format(new_version))
        raise

    # generate the new changelog
    ctx.run(
        "reno --rel-notes-dir releasenotes-installscript report \
            --ignore-cache \
            --version installscript-{} \
            --no-show-source > /tmp/new_changelog-installscript.rst".format(
            new_version
        )
    )

    # reseting git
    ctx.run("git reset --hard HEAD")

    # mac's `sed` has a different syntax for the "-i" paramter
    sed_i_arg = "-i"
    if sys.platform == 'darwin':
        sed_i_arg = "-i ''"
    # remove the old header from the existing changelog
    ctx.run("sed {0} -e '1,4d' CHANGELOG-INSTALLSCRIPT.rst".format(sed_i_arg))

    if sys.platform != 'darwin':
        # sed on darwin doesn't support `-z`. On mac, you will need to manually update the following.
        ctx.run(
            "sed -z {0} -e 's/installscript-{1}\\n===={2}/{1}\\n{2}/' /tmp/new_changelog-installscript.rst".format(
                sed_i_arg, new_version, '=' * len(new_version)
            )
        )

    # merging to CHANGELOG-INSTALLSCRIPT.rst
    ctx.run(
        "cat CHANGELOG-INSTALLSCRIPT.rst >> /tmp/new_changelog-installscript.rst && mv /tmp/new_changelog-installscript.rst CHANGELOG-INSTALLSCRIPT.rst"
    )

    # commit new CHANGELOG-INSTALLSCRIPT
    ctx.run("git add CHANGELOG-INSTALLSCRIPT.rst")

    print("\nCommit this with:")
    print("git commit -m \"[INSTALLSCRIPT] Update CHANGELOG-INSTALLSCRIPT for {}\"".format(new_version))


@task
def _find_v6_tag(ctx, v7_tag):
    """
    Returns one of the v6 tags that point at the same commit as the passed v7 tag.
    If none are found, returns the empty string.
    """
    v6_tag = ""

    print("Looking for a v6 tag pointing to same commit as tag '{}'...".format(v7_tag))
    # Find commit at which the v7_tag points
    commit = ctx.run("git rev-list --max-count=1 {}".format(v7_tag), hide='out').stdout.strip()
    try:
        v6_tags = (
            ctx.run("git tag --points-at {} | grep -E '^6\\.'".format(commit), hide='out').stdout.strip().split("\n")
        )
    except Failure:
        print("Found no v6 tag pointing at same commit as '{}'.".format(v7_tag))
    else:
        v6_tag = v6_tags[0]
        if len(v6_tags) > 1:
            print("Found v6 tags '{}', picking {}'".format(v6_tags, v6_tag))
        else:
            print("Found v6 tag '{}'".format(v6_tag))

    return v6_tag


@task
def list_major_change(_, milestone):
    """
    List all PR labeled "major_changed" for this release.
    """

    github_token = get_github_token()

    response = _query_github_api(
        github_token,
        "https://api.github.com/search/issues?q=repo:datadog/datadog-agent+label:major_change+milestone:{}".format(
            milestone
        ),
    )
    results = response.json()
    if not results["items"]:
        print("no major change for {}".format(milestone))
        return

    for pr in results["items"]:
        print("#{}: {} ({})".format(pr["number"], pr["title"], pr["html_url"]))


#
# release.json manipulation invoke tasks section
#

##
## I/O functions
##


def _load_release_json():
    with open("release.json", "r") as release_json_stream:
        return json.load(release_json_stream, object_pairs_hook=OrderedDict)


def _save_release_json(release_json):
    with open("release.json", "w") as release_json_stream:
        # Note, no space after the comma
        json.dump(release_json, release_json_stream, indent=4, sort_keys=False, separators=(',', ': '))


##
## Utils
##


def _create_version_from_match(match):
    groups = match.groups()
    version = Version(
        major=int(groups[1]),
        minor=int(groups[2]),
        patch=int(groups[4]) if groups[4] and groups[4] != 0 else None,
        devel=True if groups[5] else False,
        rc=int(groups[7]) if groups[7] and groups[7] != 0 else None,
        prefix=groups[0] if groups[0] else "",
    )
    return version


def _stringify_config(config_dict):
    """
    Takes a config dict of the following form:
    {
        "xxx_VERSION": Version(major: x, minor: y, patch: z, rc: t, prefix: "pre"),
        "xxx_HASH": "hashvalue",
        ...
    }

    and transforms all VERSIONs into their string representation (using the Version object's __str__).
    """
    return {key: str(value) for key, value in config_dict.items()}


def _query_github_api(auth_token, url):
    import requests

    # Basic auth doesn't seem to work with private repos, so we use token auth here
    headers = {"Authorization": "token {}".format(auth_token)}
    response = requests.get(url, headers=headers)
    return response


def build_compatible_version_re(allowed_major_versions, minor_version):
    """
    Returns a regex that matches only versions whose major version is
    in the provided list of allowed_major_versions, and whose minor version matches
    the provided minor version.
    """
    return re.compile(
        r'(v)?({})[.]({})([.](\d+))?(-devel)?(-rc\.(\d+))?'.format("|".join(allowed_major_versions), minor_version)
    )


##
## Base functions to fetch candidate versions on other repositories
##


def _get_highest_repo_version(
    auth, repo, version_prefix, version_re, allowed_major_versions=None, max_version: Version = None
):
    # If allowed_major_versions is not specified, search for all versions by using an empty
    # major version prefix.
    if not allowed_major_versions:
        allowed_major_versions = [""]

    highest_version = None

    for major_version in allowed_major_versions:
        url = "https://api.github.com/repos/DataDog/{}/git/matching-refs/tags/{}{}".format(
            repo, version_prefix, major_version
        )

        tags = _query_github_api(auth, url).json()

        for tag in tags:
            match = version_re.search(tag["ref"])
            if match:
                this_version = _create_version_from_match(match)
                if max_version:
                    # Get the max version that corresponds to the major version
                    # of the current tag
                    this_max_version = max_version.clone()
                    this_max_version.major = this_version.major
                    if this_version > this_max_version:
                        continue
                if this_version > highest_version:
                    highest_version = this_version

        # The allowed_major_versions are listed in order of preference
        # If something matching a given major version exists, no need to
        # go through the next ones.
        if highest_version:
            break

    if not highest_version:
        raise Exit("Couldn't find any matching {} version.".format(repo), 1)

    return highest_version


def _get_release_version_from_release_json(release_json, major_version, version_re, release_json_key=None):
    """
    If release_json_key is None, returns the highest version entry in release.json.
    If release_json_key is set, returns the entry for release_json_key of the highest version entry in release.json.
    """

    release_version = None
    release_component_version = None

    # Get the release entry for the given Agent major version
    release_entry_name = release_entry_for(major_version)
    release_json_entry = release_json.get(release_entry_name, None)

    # Check that the release entry exists, otherwise fail
    if release_json_entry:
        release_version = release_entry_name

        # Check that the component's version is defined in the release entry
        if release_json_key is not None:
            match = version_re.match(release_json_entry.get(release_json_key, ""))
            if match:
                release_component_version = _create_version_from_match(match)
            else:
                print(
                    "{} does not have a valid {} ({}), ignoring".format(
                        release_entry_name, release_json_key, release_json_entry.get(release_json_key, "")
                    )
                )

    if not release_version:
        raise Exit("Couldn't find any matching {} version.".format(release_version), 1)

    if release_json_key is not None:
        return release_component_version

    return release_version


##
## Variables needed for the repository version fetch functions
##

# COMPATIBLE_MAJOR_VERSIONS lists the major versions of tags
# that can be used with a given Agent version
# This is here for compatibility and simplicity reasons, as in most repos
# we don't create both 6 and 7 tags for a combined Agent 6 & 7 release.
# The order matters, eg. when fetching matching tags for an Agent 6 entry,
# tags starting with 6 will be preferred to tags starting with 7.
COMPATIBLE_MAJOR_VERSIONS = {6: ["6", "7"], 7: ["7"]}


# Message templates for the below functions
# Defined here either because they're long and would make the code less legible,
# or because they're used multiple times.
DIFFERENT_TAGS_TEMPLATE = (
    "The latest version of {} ({}) does not match the version used in the previous release entry ({})."
)
TAG_NOT_FOUND_TEMPLATE = "Couldn't find a(n) {} version compatible with the new Agent version entry {}"
RC_TAG_QUESTION_TEMPLATE = "The {} tag found is an RC tag: {}. Are you sure you want to use it?"
TAG_FOUND_TEMPLATE = "The {} tag is {}"


##
## Repository version fetch functions
## The following functions aim at returning the correct version to use for a given
## repository, after compatibility & user confirmations
## The version object returned by such functions should be ready to be used to fill
## the release.json entry.
##


def _fetch_dependency_repo_version(
    ctx, repo_name, new_agent_version, allowed_major_versions, compatible_version_re, github_token, check_for_rc
):
    """
    Fetches the latest tag on a given repository whose version scheme matches the one used for the Agent,
    with the following constraints:
    - the tag must have a major version that's in allowed_major_versions
    - the tag must match compatible_version_re (the main usage is to restrict the compatible tags to the
      ones with the same minor version as the Agent)?

    If check_for_rc is true, a warning will be emitted if the latest version that satisfies
    the constraints is an RC. User confirmation is then needed to check that this is desired.
    """

    # Get the highest repo version that's not higher than the Agent version we're going to build
    # We don't want to use a tag on dependent repositories that is supposed to be used in a future
    # release of the Agent (eg. if 7.31.1-rc.1 is tagged on integrations-core while we're releasing 7.30.0).
    max_allowed_version = next_final_version(ctx, new_agent_version.major)
    version = _get_highest_repo_version(
        github_token,
        repo_name,
        new_agent_version.prefix,
        compatible_version_re,
        allowed_major_versions,
        max_version=max_allowed_version,
    )

    if check_for_rc and version.is_rc():
        if not yes_no_question(RC_TAG_QUESTION_TEMPLATE.format(repo_name, version), "orange", False):
            raise Exit("Aborting release.json update.", 1)

    print(TAG_FOUND_TEMPLATE.format(repo_name, version))
    return version


def _confirm_independent_dependency_repo_version(repo, latest_version, highest_release_json_version):
    """
    Checks if the two versions of a repository we found (from release.json and from the available repo tags)
    are different. If they are, asks the user for confirmation before updating the version.
    """

    if latest_version == highest_release_json_version:
        return highest_release_json_version

    print(color_message(DIFFERENT_TAGS_TEMPLATE.format(repo, latest_version, highest_release_json_version), "orange"))
    if yes_no_question("Do you want to update {} to {}?".format(repo, latest_version), "orange", False):
        return latest_version

    return highest_release_json_version


def _fetch_independent_dependency_repo_version(
    repo_name, release_json, agent_major_version, github_token, release_json_key
):
    """
    Fetches the latest tag on a given repository whose version scheme doesn't match the one used for the Agent:
    - first, we get the latest version used in release entries of the matching Agent major version
    - then, we fetch the latest version available in the repository
    - if the above two versions are different, emit a warning and ask for user confirmation before updating the version.
    """

    previous_version = _get_release_version_from_release_json(
        release_json,
        agent_major_version,
        VERSION_RE,
        release_json_key=release_json_key,
    )
    # NOTE: This assumes that the repository doesn't change the way it prefixes versions.
    version = _get_highest_repo_version(github_token, repo_name, previous_version.prefix, VERSION_RE)

    version = _confirm_independent_dependency_repo_version(repo_name, version, previous_version)
    print(TAG_FOUND_TEMPLATE.format(repo_name, version))

    return version


def _get_windows_ddnpm_release_json_info(release_json, agent_major_version, is_first_rc=False):
    """
    Gets the Windows NPM driver info from the previous entries in the release.json file.
    """

    # First RC should use the data from nightly section otherwise reuse the last RC info
    if is_first_rc:
        previous_release_json_version = nightly_entry_for(agent_major_version)
    else:
        previous_release_json_version = release_entry_for(agent_major_version)

    print("Using '{}' DDNPM values".format(previous_release_json_version))
    release_json_version_data = release_json[previous_release_json_version]

    win_ddnpm_driver = release_json_version_data['WINDOWS_DDNPM_DRIVER']
    win_ddnpm_version = release_json_version_data['WINDOWS_DDNPM_VERSION']
    win_ddnpm_shasum = release_json_version_data['WINDOWS_DDNPM_SHASUM']

    if win_ddnpm_driver not in ['release-signed', 'attestation-signed']:
        print("WARN: WINDOWS_DDNPM_DRIVER value '{}' is not valid".format(win_ddnpm_driver))

    print("The windows ddnpm version is {}".format(win_ddnpm_version))

    return win_ddnpm_driver, win_ddnpm_version, win_ddnpm_shasum


##
## release_json object update function
##


def _update_release_json_entry(
    release_json,
    release_entry,
    integrations_version,
    omnibus_software_version,
    omnibus_ruby_version,
    jmxfetch_version,
    security_agent_policies_version,
    macos_build_version,
    windows_ddnpm_driver,
    windows_ddnpm_version,
    windows_ddnpm_shasum,
):
    """
    Adds a new entry to provided release_json object with the provided parameters, and returns the new release_json object.
    """
    import requests

    jmxfetch = requests.get(
        "https://oss.sonatype.org/service/local/repositories/releases/content/com/datadoghq/jmxfetch/{0}/jmxfetch-{0}-jar-with-dependencies.jar".format(
            jmxfetch_version,
        )
    ).content
    jmxfetch_sha256 = hashlib.sha256(jmxfetch).hexdigest()

    print("Jmxfetch's SHA256 is {}".format(jmxfetch_sha256))
    print("Windows DDNPM's SHA256 is {}".format(windows_ddnpm_shasum))

    new_version_config = OrderedDict()
    new_version_config["INTEGRATIONS_CORE_VERSION"] = integrations_version
    new_version_config["OMNIBUS_SOFTWARE_VERSION"] = omnibus_software_version
    new_version_config["OMNIBUS_RUBY_VERSION"] = omnibus_ruby_version
    new_version_config["JMXFETCH_VERSION"] = jmxfetch_version
    new_version_config["JMXFETCH_HASH"] = jmxfetch_sha256
    new_version_config["SECURITY_AGENT_POLICIES_VERSION"] = security_agent_policies_version
    new_version_config["MACOS_BUILD_VERSION"] = macos_build_version
    new_version_config["WINDOWS_DDNPM_DRIVER"] = windows_ddnpm_driver
    new_version_config["WINDOWS_DDNPM_VERSION"] = windows_ddnpm_version
    new_version_config["WINDOWS_DDNPM_SHASUM"] = windows_ddnpm_shasum

    # Necessary if we want to maintain the JSON order, so that humans don't get confused
    new_release_json = OrderedDict()

    # Add all versions from the old release.json
    for key, value in release_json.items():
        new_release_json[key] = value

    # Then update the entry
    new_release_json[release_entry] = _stringify_config(new_version_config)

    return new_release_json


##
## Main functions
##


def _update_release_json(ctx, release_json, release_entry, new_version: Version, github_token):
    """
    Updates the provided release.json object by fetching compatible versions for all dependencies
    of the provided Agent version, constructing the new entry, adding it to the release.json object
    and returning it.
    """

    allowed_major_versions = COMPATIBLE_MAJOR_VERSIONS[new_version.major]

    # Part 1: repositories which follow the Agent version scheme

    # For repositories which follow the Agent version scheme, we want to only get
    # tags with the same minor version, to avoid problems when releasing a patch
    # version while a minor version release is ongoing.
    compatible_version_re = build_compatible_version_re(allowed_major_versions, new_version.minor)

    # If the new version is a final version, set the check_for_rc flag to true to warn if a dependency's version
    # is an RC.
    check_for_rc = not new_version.is_rc()

    integrations_version = _fetch_dependency_repo_version(
        ctx, "integrations-core", new_version, allowed_major_versions, compatible_version_re, github_token, check_for_rc
    )

    omnibus_software_version = _fetch_dependency_repo_version(
        ctx, "omnibus-software", new_version, allowed_major_versions, compatible_version_re, github_token, check_for_rc
    )

    omnibus_ruby_version = _fetch_dependency_repo_version(
        ctx, "omnibus-ruby", new_version, allowed_major_versions, compatible_version_re, github_token, check_for_rc
    )

    macos_build_version = _fetch_dependency_repo_version(
        ctx,
        "datadog-agent-macos-build",
        new_version,
        allowed_major_versions,
        compatible_version_re,
        github_token,
        check_for_rc,
    )

    # Part 2: repositories which have their own version scheme
    jmxfetch_version = _fetch_independent_dependency_repo_version(
        "jmxfetch", release_json, new_version.major, github_token, "JMXFETCH_VERSION"
    )

    security_agent_policies_version = _fetch_independent_dependency_repo_version(
        "security-agent-policies", release_json, new_version.major, github_token, "SECURITY_AGENT_POLICIES_VERSION"
    )

    windows_ddnpm_driver, windows_ddnpm_version, windows_ddnpm_shasum = _get_windows_ddnpm_release_json_info(
        release_json, new_version.major, is_first_rc=(new_version.rc == 1)
    )

    # Add new entry to the release.json object and return it
    return _update_release_json_entry(
        release_json,
        release_entry,
        integrations_version,
        omnibus_software_version,
        omnibus_ruby_version,
        jmxfetch_version,
        security_agent_policies_version,
        macos_build_version,
        windows_ddnpm_driver,
        windows_ddnpm_version,
        windows_ddnpm_shasum,
    )


def update_release_json(ctx, github_token, new_version: Version):
    """
    Updates the release entries in release.json to prepare the next RC or final build.
    """
    release_json = _load_release_json()

    release_entry = release_entry_for(new_version.major)
    print("Updating {} for {}".format(release_entry, new_version))

    # Update release.json object with the entry for the new version
    release_json = _update_release_json(ctx, release_json, release_entry, new_version, github_token)

    _save_release_json(release_json)


def check_version(agent_version):
    """Check Agent version to see if it is valid."""
    version_re = re.compile(r'7[.](\d+)[.](\d+)(-rc\.(\d+))?')
    if not version_re.match(agent_version):
        raise Exit(message="Version should be of the form 7.Y.Z or 7.Y.Z-rc.t")


@task
def update_modules(ctx, agent_version, verify=True):
    """
    Update internal dependencies between the different Agent modules.
    * --verify checks for correctness on the Agent Version (on by default).

    Examples:
    inv -e release.update-modules 7.27.0
    """
    if verify:
        check_version(agent_version)

    for module in DEFAULT_MODULES.values():
        for dependency in module.dependencies:
            dependency_mod = DEFAULT_MODULES[dependency]
            ctx.run(
                "go mod edit -require={dependency_path} {go_mod_path}".format(
                    dependency_path=dependency_mod.dependency_path(agent_version), go_mod_path=module.go_mod_path()
                )
            )


@task
def tag_version(ctx, agent_version, commit="HEAD", verify=True, push=True, force=False):
    """
    Create tags for a given Datadog Agent version.
    The version should be given as an Agent 7 version.

    * --commit COMMIT will tag COMMIT with the tags (default HEAD)
    * --verify checks for correctness on the Agent version (on by default).
    * --push will push the tags to the origin remote (on by default).
    * --force will allow the task to overwrite existing tags. Needed to move existing tags (off by default).

    Examples:
    inv -e release.tag-version 7.27.0                 # Create tags and push them to origin
    inv -e release.tag-version 7.27.0-rc.3 --no-push  # Create tags locally; don't push them
    inv -e release.tag-version 7.29.0-rc.3 --force    # Create tags (overwriting existing tags with the same name), force-push them to origin
    """
    if verify:
        check_version(agent_version)

    force_option = ""
    if force:
        print(color_message("--force option enabled. This will allow the task to overwrite existing tags.", "orange"))
        result = yes_no_question("Please confirm the use of the --force option.", color="orange", default=False)
        if result:
            print("Continuing with the --force option.")
            force_option = " --force"
        else:
            print("Continuing without the --force option.")

    for module in DEFAULT_MODULES.values():
        if module.should_tag:
            for tag in module.tag(agent_version):
                ok = try_git_command(
                    ctx,
                    "git tag -m {tag} {tag} {commit}{force_option}".format(
                        tag=tag, commit=commit, force_option=force_option
                    ),
                )
                if not ok:
                    message = f"Could not create tag {tag}. Please rerun the task to retry creating the tags (you may need the --force option)"
                    raise Exit(color_message(message, "red"), code=1)
                print("Created tag {tag}".format(tag=tag))
                if push:
                    ctx.run("git push origin {tag}{force_option}".format(tag=tag, force_option=force_option))
                    print("Pushed tag {tag}".format(tag=tag))

    print("Created all tags for version {}".format(agent_version))


def current_version(ctx, major_version) -> Version:
    return _create_version_from_match(VERSION_RE.search(get_version(ctx, major_version=major_version)))


def next_final_version(ctx, major_version) -> Version:
    previous_version = current_version(ctx, major_version)

    # Set the new version
    if previous_version.is_devel():
        # If the previous version was a devel version, use the same version without devel
        # (should never happen during regular releases, we always do at least one RC)
        return previous_version.non_devel_version()

    return previous_version.next_version(rc=False)


def next_rc_version(ctx, major_version, patch_version=False) -> Version:
    # Fetch previous version from the most recent tag on the branch
    previous_version = current_version(ctx, major_version)

    if previous_version.is_rc():
        # We're already on an RC, only bump the RC version
        new_version = previous_version.next_version(rc=True)
    else:
        if patch_version:
            new_version = previous_version.next_version(bump_patch=True, rc=True)
        else:
            # Minor version bump, we're doing a standard release:
            # - if the previous tag is a devel tag, use it without the devel tag
            # - otherwise (should not happen during regular release cycles), bump the minor version
            if previous_version.is_devel():
                new_version = previous_version.non_devel_version()
                new_version = new_version.next_version(rc=True)
            else:
                new_version = previous_version.next_version(bump_minor=True, rc=True)

    return new_version


def check_base_branch(branch, release_version):
    """
    Checks if the given branch is either the default branch or the release branch associated
    with the given release version.
    """
    return branch == DEFAULT_BRANCH or branch == release_version.branch()


def check_uncommitted_changes(ctx):
    """
    Checks if there are uncommitted changes in the local git repository.
    """
    modified_files = ctx.run("git --no-pager diff --name-only HEAD | wc -l", hide=True).stdout.strip()

    # Return True if at least one file has uncommitted changes.
    return modified_files != "0"


def check_local_branch(ctx, branch):
    """
    Checks if the given branch exists locally
    """
    matching_branch = ctx.run("git --no-pager branch --list {} | wc -l".format(branch), hide=True).stdout.strip()

    # Return True if a branch is returned by git branch --list
    return matching_branch != "0"


def check_upstream_branch(github, branch):
    """
    Checks if the given branch already exists in the upstream repository
    """
    github_branch = github.get_branch(branch)

    # Return True if the branch exists
    return github_branch and github_branch.get('name', False)


def parse_major_versions(major_versions):
    return sorted(int(x) for x in major_versions.split(","))


def try_git_command(ctx, git_command):
    """
    Try a git command that should be retried (after user confirmation) if it fails.
    Primarily useful for commands which can fail if commit signing fails: we don't want the
    whole workflow to fail if that happens, we want to retry.
    """

    do_retry = True

    while do_retry:
        res = ctx.run(git_command, warn=True)
        if res.exited is None or res.exited > 0:
            print(
                color_message(
                    "Failed to run \"{}\" (did the commit/tag signing operation fail?)".format(git_command),
                    "orange",
                )
            )
            do_retry = yes_no_question("Do you want to retry this operation?", color="orange", default=True)
            continue

        return True

    return False


@task
def finish(ctx, major_versions="6,7"):
    """
    Updates the release entry in the release.json file for the new version.

    Updates internal module dependencies with the new version.
    """

    if sys.version_info[0] < 3:
        return Exit(message="Must use Python 3 for this task", code=1)

    list_major_versions = parse_major_versions(major_versions)
    print("Finishing release for major version(s) {}".format(list_major_versions))

    github_token = get_github_token()

    for major_version in list_major_versions:
        new_version = next_final_version(ctx, major_version)
        update_release_json(github_token, new_version)

    # Update internal module dependencies
    update_modules(ctx, str(new_version))


@task(help={'upstream': "Remote repository name (default 'origin')"})
def create_rc(ctx, major_versions="6,7", patch_version=False, upstream="origin"):
    """
    Updates the release entries in release.json to prepare the next RC build.
    If the previous version of the Agent (determined as the latest tag on the
    current branch) is not an RC:
    - by default, updates the release entries for the next minor version of
      the Agent.
    - if --patch-version is specified, updates the release entries for the next
      patch version of the Agent.

    This changes which tags will be considered on the dependency repositories (only
    tags that match the same major and minor version as the Agent).

    If the previous version of the Agent was an RC, updates the release entries for RC + 1.

    Examples:
    If the latest tag on the branch is 7.31.0, and invoke release.create-rc --patch-version
    is run, then the task will prepare the release entries for 7.31.1-rc.1, and therefore
    will only use 7.31.X tags on the dependency repositories that follow the Agent version scheme.

    If the latest tag on the branch is 7.32.0-devel or 7.31.0, and invoke release.create-rc
    is run, then the task will prepare the release entries for 7.32.0-rc.1, and therefore
    will only use 7.32.X tags on the dependency repositories that follow the Agent version scheme.

    Updates internal module dependencies with the new RC.

    Commits the above changes, and then creates a PR on the upstream repository with the change.

    Notes:
    This requires a Github token (either in the GITHUB_TOKEN environment variable, or in the MacOS keychain),
    with 'repo' permissions.
    This also requires that there are no local uncommitted changes, that the current branch is 'main' or the
    release branch, and that no branch named 'release/<new rc version>' already exists locally or upstream.
    """
    if sys.version_info[0] < 3:
        return Exit(message="Must use Python 3 for this task", code=1)

    github = GithubAPI(repository=REPOSITORY_NAME, api_token=get_github_token())

    list_major_versions = parse_major_versions(major_versions)

    # Get the version of the highest major: useful for some logging & to get
    # the version to use for Go submodules updates
    new_highest_version = next_rc_version(ctx, max(list_major_versions), patch_version)

    # Get a string representation of the RC, eg. "6/7.32.0-rc.1"
    versions_string = "{}".format(
        "/".join([str(n) for n in list_major_versions[:-1]] + [str(new_highest_version)]),
    )

    print(color_message("Preparing RC for agent version(s) {}".format(list_major_versions), "bold"))

    # Step 0: checks

    print(color_message("Checking repository state", "bold"))
    ctx.run("git fetch")

    if check_uncommitted_changes(ctx):
        raise Exit(
            color_message(
                "There are uncomitted changes in your repository. Please commit or stash them before trying again.",
                "red",
            ),
            code=1,
        )

    # Check that the current and update branches are valid
    current_branch = ctx.run("git rev-parse --abbrev-ref HEAD", hide=True).stdout.strip()
    update_branch = "release/{}".format(new_highest_version)

    if not check_base_branch(current_branch, new_highest_version):
        raise Exit(
            color_message(
                "The branch you are on is neither {} or the correct release branch ({}). Aborting.".format(
                    DEFAULT_BRANCH, new_highest_version.branch()
                ),
                "red",
            ),
            code=1,
        )

    if check_local_branch(ctx, update_branch):
        raise Exit(
            color_message(
                "The branch {} already exists locally. Please remove it before trying again.".format(update_branch),
                "red",
            ),
            code=1,
        )

    if check_upstream_branch(github, update_branch):
        raise Exit(
            color_message(
                "The branch {} already exists upstream. Please remove it before trying again.".format(update_branch),
                "red",
            ),
            code=1,
        )

    # Find milestone based on what the next final version is. If the milestone does not exist, fail.
    milestone_name = str(next_final_version(ctx, max(list_major_versions)))

    milestone = github.get_milestone_by_name(milestone_name)

    if not milestone or not milestone.get("number"):
        raise Exit(
            color_message(
                """Could not find milestone {} in the Github repository. Response: {}
Make sure that milestone is open before trying again.""".format(
                    milestone_name, milestone
                ),
                "red",
            ),
            code=1,
        )

    # Step 1: Update release entries

    print(color_message("Updating release entries", "bold"))
    for major_version in list_major_versions:
        new_version = next_rc_version(ctx, major_version, patch_version)
        update_release_json(ctx, github.api_token, new_version)

    # Step 2: Update internal module dependencies

    print(color_message("Updating Go modules", "bold"))
    update_modules(ctx, str(new_highest_version))

    # Step 3: branch out, commit change, push branch

    print(color_message("Branching out to {}".format(update_branch), "bold"))
    ctx.run("git checkout -b {}".format(update_branch))

    print(color_message("Committing release.json and Go modules updates", "bold"))
    print(
        color_message(
            "If commit signing is enabled, you will have to make sure the commit gets properly signed.", "bold"
        )
    )
    ctx.run("git add release.json")
    ctx.run("git ls-files . | grep 'go.mod$' | xargs git add")

    ok = try_git_command(ctx, "git commit -m 'Update release.json and Go modules for {}'".format(versions_string))
    if not ok:
        raise Exit(
            color_message(
                "Could not create commit. Please commit manually, push the {} branch and then open a PR against {}.".format(
                    update_branch,
                    current_branch,
                ),
                "red",
            ),
            code=1,
        )

    print(color_message("Pushing new branch to the upstream repository", "bold"))
    res = ctx.run("git push --set-upstream {} {}".format(upstream, update_branch), warn=True)
    if res.exited is None or res.exited > 0:
        raise Exit(
            color_message(
                "Could not push branch {} to the upstream '{}'. Please push it manually and then open a PR against {}.".format(
                    update_branch,
                    upstream,
                    current_branch,
                ),
                "red",
            ),
            code=1,
        )

    print(color_message("Creating PR", "bold"))

    # Step 4: create PR

    pr = github.create_pr(
        pr_title="[release] Update release.json and Go modules for {}".format(versions_string),
        pr_body="",
        base_branch=current_branch,
        target_branch=update_branch,
    )

    if not pr or not pr.get("number"):
        raise Exit(
            color_message("Could not create PR in the Github repository. Response: {}".format(pr), "red"),
            code=1,
        )

    print(color_message("Created PR #{}".format(pr["number"]), "bold"))

    # Step 5: add milestone and labels to PR

    updated_pr = github.update_pr(
        pull_number=pr["number"],
        milestone_number=milestone["number"],
        labels=["changelog/no-changelog", "qa/skip-qa", "team/agent-platform", "team/agent-core"],
    )

    if not updated_pr or not updated_pr.get("number") or not updated_pr.get("html_url"):
        raise Exit(
            color_message("Could not update PR in the Github repository. Response: {}".format(updated_pr), "red"),
            code=1,
        )

    print(color_message("Set labels and milestone for PR #{}".format(updated_pr["number"]), "bold"))
    print(
        color_message(
            "Done preparing RC {}. The PR is available here: {}".format(versions_string, updated_pr["html_url"]), "bold"
        )
    )


@task(help={'redo': "Redo the tag & build for the last RC that was tagged, instead of creating tags for the next RC."})
def build_rc(ctx, major_versions="6,7", patch_version=False, redo=False):
    """
    To be done after the PR created by release.create-rc is merged, with the same options
    as release.create-rc.

    Tags the new RC versions on the current commit, and creates the build pipeline for these
    new tags.
    """
    if sys.version_info[0] < 3:
        return Exit(message="Must use Python 3 for this task", code=1)

    gitlab = Gitlab(project_name=REPOSITORY_NAME, api_token=get_gitlab_token())
    list_major_versions = parse_major_versions(major_versions)

    # Get the version of the highest major: needed for tag_version and to know
    # which tag to target when creating the pipeline.
    if redo:
        # If redo is enabled, we're moving the current RC tag, so we keep the same version
        new_version = current_version(ctx, max(list_major_versions))
    else:
        new_version = next_rc_version(ctx, max(list_major_versions), patch_version)

    # Get a string representation of the RC, eg. "6/7.32.0-rc.1"
    versions_string = "{}".format(
        "/".join([str(n) for n in list_major_versions[:-1]] + [str(new_version)]),
    )

    # Step 0: checks

    print(color_message("Checking repository state", "bold"))
    # Check that the base branch is valid
    current_branch = ctx.run("git rev-parse --abbrev-ref HEAD", hide=True).stdout.strip()

    if not check_base_branch(current_branch, new_version):
        raise Exit(
            color_message(
                "The branch you are on is neither {} or the correct release branch ({}). Aborting.".format(
                    DEFAULT_BRANCH, new_version.branch()
                ),
                "red",
            ),
            code=1,
        )

    latest_commit = ctx.run("git --no-pager log --no-color -1 --oneline").stdout.strip()

    if not yes_no_question(
        "This task will create tags for {} on the current commit: {}. Is this OK?".format(
            versions_string, latest_commit
        ),
        color="orange",
        default=False,
    ):
        raise Exit(color_message("Aborting.", "red"), code=1)

    # Step 1: Tag versions

    print(color_message("Tagging RC for agent version(s) {}".format(list_major_versions), "bold"))
    print(
        color_message("If commit signing is enabled, you will have to make sure each tag gets properly signed.", "bold")
    )
    # tag_version only takes the highest version (Agent 7 currently), and creates
    # the tags for all supported versions
    # TODO: make it possible to do Agent 6-only or Agent 7-only tags?
    # Note: if redo is enabled, then we need to set the --force option to move the tags.
    tag_version(ctx, str(new_version), force=redo)

    print(color_message("Waiting until the {} tag appears in Gitlab".format(new_version), "bold"))
    gitlab_tag = None
    while not gitlab_tag:
        gitlab_tag = gitlab.find_tag(str(new_version)).get("name", None)
        sleep(5)

    print(color_message("Creating RC pipeline", "bold"))

    # Step 2: Run the RC pipeline

    run(
        ctx,
        git_ref=gitlab_tag,
        use_release_entries=True,
        major_versions=major_versions,
        repo_branch="beta",
        deploy=True,
    )


@task(help={'key': "Path to the release.json key, separated with double colons, eg. 'last_stable::6'"})
def get_release_json_value(_, key):

    release_json = _load_release_json()

    path = key.split('::')

    for element in path:
        if element not in release_json:
            raise Exit(code=1, message=f"Couldn't find '{key}' in release.json")

        release_json = release_json.get(element)

    print(release_json)
