# Unless explicitly stated otherwise all files in this repository are licensed
# under the Apache License Version 2.0.
# This product includes software developed at Datadog (https:#www.datadoghq.com/).
# Copyright 2016-present Datadog, Inc.

require './lib/ostools.rb'
require 'json'

name 'datadog-agent-integrations-py2'

dependency 'datadog-agent'
dependency 'pip2'


if arm?
  # psycopg2 doesn't come with pre-built wheel on the arm architecture.
  # to compile from source, it requires the `pg_config` executable present on the $PATH
  dependency 'postgresql'
  # same with libffi to build the cffi wheel
  dependency 'libffi'
  # same with libxml2 and libxslt to build the lxml wheel
  dependency 'libxml2'
  dependency 'libxslt'
end

if osx?
  dependency 'unixodbc'
end

if linux?
  # add nfsiostat script
  dependency 'unixodbc'
  dependency 'freetds'  # needed for SQL Server integration
  dependency 'nfsiostat'
  # add libkrb5 for all integrations supporting kerberos auth with `requests-kerberos`
  dependency 'libkrb5'
  # needed for glusterfs
  dependency 'gstatus'
end

relative_path 'integrations-core'
whitelist_file "embedded/lib/python2.7/site-packages/.libsaerospike"
whitelist_file "embedded/lib/python2.7/site-packages/psycopg2"
whitelist_file "embedded/lib/python2.7/site-packages/wrapt"
whitelist_file "embedded/lib/python2.7/site-packages/pymqi"

source git: 'https://github.com/DataDog/integrations-core.git'

integrations_core_version = ENV['INTEGRATIONS_CORE_VERSION']
if integrations_core_version.nil? || integrations_core_version.empty?
  integrations_core_version = 'master'
end
default_version integrations_core_version

# folder names containing integrations from -core that won't be packaged with the Agent
blacklist_folders = [
  'datadog_checks_base',           # namespacing package for wheels (NOT AN INTEGRATION)
  'datadog_checks_dev',            # Development package, (NOT AN INTEGRATION)
  'datadog_checks_tests_helper',   # Testing and Development package, (NOT AN INTEGRATION)
  'docker_daemon',                 # Agent v5 only
]

# package names of dependencies that won't be added to the Agent Python environment
blacklist_packages = Array.new


if suse?
  # Temporarily blacklist Aerospike until builder supports new dependency
  blacklist_packages.push(/^aerospike==/)
  blacklist_folders.push('aerospike')
end

if osx?
  # Blacklist lxml as it fails MacOS notarization: the etree.cpython-37m-darwin.so and objectify.cpython-37m-darwin.so
  # binaries were built with a MacOS SDK lower than 10.9.
  # This can be removed once a new lxml version with binaries built with a newer SDK is available.
  blacklist_packages.push(/^lxml==/)

  # Blacklist ibm_was, which depends on lxml
  blacklist_folders.push('ibm_was')

  # Blacklist aerospike, new version 3.10 is not supported on MacOS yet
  blacklist_folders.push('aerospike')

  # Temporarily blacklist Aerospike until builder supports new dependency
  blacklist_packages.push(/^aerospike==/)
  blacklist_folders.push('aerospike')
end

if arm?
  # Temporarily blacklist Aerospike until builder supports new dependency
  blacklist_folders.push('aerospike')
  blacklist_packages.push(/^aerospike==/)

  # This doesn't build on ARM
  blacklist_folders.push('ibm_mq')
  blacklist_packages.push(/^pymqi==/)
end

if arm? || !_64_bit?
  blacklist_packages.push(/^orjson==/)
end

final_constraints_file = 'final_constraints-py2.txt'
agent_requirements_file = 'agent_requirements-py2.txt'
filtered_agent_requirements_in = 'agent_requirements-py2.in'
agent_requirements_in = 'agent_requirements.in'

build do
  # The dir for confs
  if osx?
    conf_dir = "#{install_dir}/etc/conf.d"
  else
    conf_dir = "#{install_dir}/etc/datadog-agent/conf.d"
  end
  mkdir conf_dir

  # aliases for pip
  if windows?
    pip = "#{windows_safe_path(python_2_embedded)}\\Scripts\\pip.exe"
    python = "#{windows_safe_path(python_2_embedded)}\\python.exe"
  else
    pip = "#{install_dir}/embedded/bin/pip2"
    python = "#{install_dir}/embedded/bin/python2"
  end

  # Install the checks along with their dependencies
  block do
    #
    # Prepare the build env, these dependencies are only needed to build and
    # install the core integrations.
    #
    command "#{pip} install wheel==0.34.1"
    command "#{pip} install setuptools-scm==5.0.2" # Pin to the last version that supports Python 2
    command "#{pip} install pip-tools==5.4.0"
    uninstall_buildtime_deps = ['rtloader', 'click', 'first', 'pip-tools']
    nix_build_env = {
      "CFLAGS" => "-I#{install_dir}/embedded/include -I/opt/mqm/inc",
      "CXXFLAGS" => "-I#{install_dir}/embedded/include -I/opt/mqm/inc",
      "LDFLAGS" => "-L#{install_dir}/embedded/lib -L/opt/mqm/lib64 -L/opt/mqm/lib",
      "LD_RUN_PATH" => "#{install_dir}/embedded/lib -L/opt/mqm/lib64 -L/opt/mqm/lib",
      "PATH" => "#{install_dir}/embedded/bin:#{ENV['PATH']}",
    }

    # On Linux & Windows, specify the C99 standard explicitly to avoid issues while building some
    # wheels (eg. ddtrace).
    # Not explicitly setting that option has caused us problems in the past on SUSE, where the ddtrace
    # wheel has to be manually built, as the C code in ddtrace doesn't follow the C89 standard (the default value of std).
    # Note: We don't set this on MacOS, as on MacOS we need to build a bunch of packages & C extensions that
    # don't have precompiled MacOS wheels. When building C extensions, the CFLAGS variable is added to
    # the command-line parameters, even when compiling C++ code, where -std=c99 is invalid.
    # See: https://github.com/python/cpython/blob/v2.7.18/Lib/distutils/sysconfig.py#L222
    if linux? || windows?
      nix_build_env["CFLAGS"] += " -std=c99"
    end

    #
    # Prepare the requirements file containing ALL the dependencies needed by
    # any integration. This will provide the "static Python environment" of the Agent.
    # We don't use the .in file provided by the base check directly because we
    # want to filter out things before installing.
    #
    if windows?
      static_reqs_in_file = "#{windows_safe_path(project_dir)}\\datadog_checks_base\\datadog_checks\\base\\data\\#{agent_requirements_in}"
      static_reqs_out_file = "#{windows_safe_path(project_dir)}\\#{filtered_agent_requirements_in}"
    else
      static_reqs_in_file = "#{project_dir}/datadog_checks_base/datadog_checks/base/data/#{agent_requirements_in}"
      static_reqs_out_file = "#{project_dir}/#{filtered_agent_requirements_in}"
    end

    # Remove any blacklisted requirements from the static-environment req file
    requirements = Array.new
    File.open("#{static_reqs_in_file}", 'r+').readlines().each do |line|
      blacklist_flag = false
      blacklist_packages.each do |blacklist_regex|
        re = Regexp.new(blacklist_regex).freeze
        if re.match line
          blacklist_flag = true
        end
      end

      if !blacklist_flag
        requirements.push(line)
      end
    end

    # Adding pympler for memory debug purposes
    requirements.push("pympler==0.7")

    # Render the filtered requirements file
    erb source: "static_requirements.txt.erb",
        dest: "#{static_reqs_out_file}",
        mode: 0640,
        vars: { requirements: requirements }

    # Use pip-compile to create the final requirements file. Notice when we invoke `pip` through `python -m pip <...>`,
    # there's no need to refer to `pip`, the interpreter will pick the right script.
    if windows?
      wheel_build_dir = "#{windows_safe_path(project_dir)}\\.wheels"
      command "#{python} -m pip wheel . --no-deps --no-index --wheel-dir=#{wheel_build_dir}", :cwd => "#{windows_safe_path(project_dir)}\\datadog_checks_base"
      command "#{python} -m pip install datadog_checks_base --no-deps --no-index --find-links=#{wheel_build_dir}"
      command "#{python} -m pip wheel . --no-deps --no-index --wheel-dir=#{wheel_build_dir}", :cwd => "#{windows_safe_path(project_dir)}\\datadog_checks_downloader"
      command "#{python} -m pip install datadog_checks_downloader --no-deps --no-index --find-links=#{wheel_build_dir}"
      command "#{python} -m piptools compile --generate-hashes --output-file #{windows_safe_path(install_dir)}\\#{agent_requirements_file} #{static_reqs_out_file}"
    else
      wheel_build_dir = "#{project_dir}/.wheels"
      command "#{pip} wheel . --no-deps --no-index --wheel-dir=#{wheel_build_dir}", :env => nix_build_env, :cwd => "#{project_dir}/datadog_checks_base"
      command "#{pip} install datadog_checks_base --no-deps --no-index --find-links=#{wheel_build_dir}"
      command "#{pip} wheel . --no-deps --no-index --wheel-dir=#{wheel_build_dir}", :env => nix_build_env, :cwd => "#{project_dir}/datadog_checks_downloader"
      command "#{pip} install datadog_checks_downloader --no-deps --no-index --find-links=#{wheel_build_dir}"
      command "#{python} -m piptools compile --generate-hashes --output-file #{install_dir}/#{agent_requirements_file} #{static_reqs_out_file}", :env => nix_build_env
    end

    # From now on we don't need piptools anymore, uninstall its deps so we don't include them in the final artifact
    uninstall_buildtime_deps.each do |dep|
      if windows?
        command "#{python} -m pip uninstall -y #{dep}"
      else
        command "#{pip} uninstall -y #{dep}"
      end
    end

    #
    # Install static-environment requirements that the Agent and all checks will use
    #
    if windows?
      command "#{python} -m pip install --no-deps --require-hashes -r #{windows_safe_path(install_dir)}\\#{agent_requirements_file}"
    else
      command "#{pip} install --no-deps --require-hashes -r #{install_dir}/#{agent_requirements_file}", :env => nix_build_env
    end

    #
    # Install Core integrations
    #

    # Create a constraint file after installing all the core dependencies and before any integration
    # This is then used as a constraint file by the integration command to avoid messing with the agent's python environment
    command "#{pip} freeze > #{install_dir}/#{final_constraints_file}"

    if windows?
        cached_wheels_dir = "#{windows_safe_path(wheel_build_dir)}\\.cached"
    else
        cached_wheels_dir = "#{wheel_build_dir}/.cached"
    end
    checks_to_install = Array.new

    # Go through every integration package in `integrations-core`, build and install
    Dir.glob("#{project_dir}/*").each do |check_dir|
      check = check_dir.split('/').last

      # do not install blacklisted integrations
      next if !File.directory?("#{check_dir}") || blacklist_folders.include?(check)

      # If there is no manifest file, then we should assume the folder does not
      # contain a working check and move onto the next
      manifest_file_path = "#{check_dir}/manifest.json"

      # If there is no manifest file, then we should assume the folder does not
      # contain a working check and move onto the next
      File.exist?(manifest_file_path) || next

      manifest = JSON.parse(File.read(manifest_file_path))
      manifest['supported_os'].include?(os) || next

      setup_file_path = "#{check_dir}/setup.py"
      File.file?(setup_file_path) || next
      # Check if it supports Python 2.
      support = `inv agent.check-supports-python-version #{setup_file_path} 2`
      if support == "False"
        log.info(log_key) { "Skipping '#{check}' since it does not support Python 2." }
        next
      end

      checks_to_install.push(check)
    end

    tasks_dir_in = windows_safe_path(Dir.pwd)
    cache_bucket = ENV.fetch('INTEGRATION_WHEELS_CACHE_BUCKET', '')
    cache_branch = /^(7\.\d+\.x|main)$/.match(ENV.fetch('CI_COMMIT_BRANCH', ''))
    # On windows, `aws` actually executes Ruby's AWS SDK, but we want the Python one
    awscli = if windows? then '"c:\program files\amazon\awscli\bin\aws"' else 'aws' end
    if cache_bucket != ''
      mkdir cached_wheels_dir
      command "inv -e agent.get-integrations-from-cache " \
        "--python 2 --bucket #{cache_bucket} " \
        "--branch #{cache_branch || 'main'} " \
        "--integrations-dir #{windows_safe_path(project_dir)} " \
        "--target-dir #{cached_wheels_dir} " \
        "--integrations #{checks_to_install.join(',')} " \
        "--awscli #{awscli}",
        :cwd => tasks_dir_in

      # install all wheels from cache in one pip invocation to speed things up
      if windows?
        command "#{python} -m pip install --no-deps --no-index " \
          "--find-links #{windows_safe_path(cached_wheels_dir)} -r #{windows_safe_path(cached_wheels_dir)}\\found.txt"
      else
        command "#{pip} install --no-deps --no-index " \
          " --find-links #{cached_wheels_dir} -r #{cached_wheels_dir}/found.txt"
      end
    end

    block do
      # we have to do this operation in block, so that it can access files created by the
      # inv agent.get-integrations-from-cache command

      # get list of integration wheels already installed from cache
      installed_list = Array.new
      if cache_bucket != ''
        if windows?
          installed_out = `#{python} -m pip list --format json`
        else
          installed_out = `#{pip} list --format json`
        end
        if $?.exitstatus == 0
          installed = JSON.parse(installed_out)
          installed.each do |package|
            package.each do |key, value|
              if key == "name" && value.start_with?("datadog-")
                installed_list.push(value["datadog-".length..-1])
              end
            end
          end
        else
          raise "Failed to list pip installed packages"
        end
      end

      checks_to_install.each do |check|
        check_dir = File.join(project_dir, check)
        check_conf_dir = "#{conf_dir}/#{check}.d"
        # For each conf file, if it already exists, that means the `datadog-agent` software def
        # wrote it first. In that case, since the agent's confs take precedence, skip the conf

        # Copy the check config to the conf directories
        conf_file_example = "#{check_dir}/datadog_checks/#{check}/data/conf.yaml.example"
        if File.exist? conf_file_example
          mkdir check_conf_dir
          copy conf_file_example, "#{check_conf_dir}/" unless File.exist? "#{check_conf_dir}/conf.yaml.example"
        end

        # Copy the default config, if it exists
        conf_file_default = "#{check_dir}/datadog_checks/#{check}/data/conf.yaml.default"
        if File.exist? conf_file_default
          mkdir check_conf_dir
          copy conf_file_default, "#{check_conf_dir}/" unless File.exist? "#{check_conf_dir}/conf.yaml.default"
        end

        # Copy the metric file, if it exists
        metrics_yaml = "#{check_dir}/datadog_checks/#{check}/data/metrics.yaml"
        if File.exist? metrics_yaml
          mkdir check_conf_dir
          copy metrics_yaml, "#{check_conf_dir}/" unless File.exist? "#{check_conf_dir}/metrics.yaml"
        end

        # We don't have auto_conf on windows yet
        auto_conf_yaml = "#{check_dir}/datadog_checks/#{check}/data/auto_conf.yaml"
        if File.exist? auto_conf_yaml
          mkdir check_conf_dir
          copy auto_conf_yaml, "#{check_conf_dir}/" unless File.exist? "#{check_conf_dir}/auto_conf.yaml"
        end

        # Copy SNMP profiles
        profiles = "#{check_dir}/datadog_checks/#{check}/data/profiles"
        if File.exist? profiles
          copy profiles, "#{check_conf_dir}/"
        end

        # pip < 21.2 replace underscores by dashes in package names per https://pip.pypa.io/en/stable/news/#v21-2
        # whether or not this might switch back in the future is not guaranteed, so we check for both name
        # with dashes and underscores
        if installed_list.include?(check) || installed_list.include?(check.gsub('_', '-'))
          next
        end

        if windows?
          command "#{python} -m pip wheel . --no-deps --no-index --wheel-dir=#{wheel_build_dir}", :cwd => "#{windows_safe_path(project_dir)}\\#{check}"
          command "#{python} -m pip install datadog-#{check} --no-deps --no-index --find-links=#{wheel_build_dir}"
        else
          command "#{pip} wheel . --no-deps --no-index --wheel-dir=#{wheel_build_dir}", :env => nix_build_env, :cwd => "#{project_dir}/#{check}"
          command "#{pip} install datadog-#{check} --no-deps --no-index --find-links=#{wheel_build_dir}"
        end
        if cache_bucket != '' && ENV.fetch('INTEGRATION_WHEELS_SKIP_CACHE_UPLOAD', '') == '' && cache_branch != nil
          command "inv -e agent.upload-integration-to-cache " \
            "--python 2 --bucket #{cache_bucket} " \
            "--branch #{cache_branch} " \
            "--integrations-dir #{windows_safe_path(project_dir)} " \
            "--build-dir #{wheel_build_dir} " \
            "--integration #{check} " \
            "--awscli #{awscli}",
            :cwd => tasks_dir_in
        end
      end
    end

    block do
      # We have to run these operations in block, so they get applied after operations
      # from the last block

      # Patch applies to only one file: set it explicitly as a target, no need for -p
      if windows?
        patch :source => "create-regex-at-runtime.patch", :target => "#{python_2_embedded}/Lib/site-packages/yaml/reader.py"
        patch :source => "tuf-0.17.0-cve-2021-41131.patch", :target => "#{python_2_embedded}/Lib/site-packages/tuf/client/updater.py"
      else
        patch :source => "create-regex-at-runtime.patch", :target => "#{install_dir}/embedded/lib/python2.7/site-packages/yaml/reader.py"
        patch :source => "tuf-0.17.0-cve-2021-41131.patch", :target => "#{install_dir}/embedded/lib/python2.7/site-packages/tuf/client/updater.py"
      end

      if linux?
        patch :source => "psutil-pr2000.patch", :target => "#{install_dir}/embedded/lib/python2.7/site-packages/psutil/_pslinux.py"
      end

      # Run pip check to make sure the agent's python environment is clean, all the dependencies are compatible
      if windows?
        command "#{python} -m pip check"
      else
        command "#{pip} check"
      end
    end
  end

  # Ship `requirements-agent-release.txt` file containing the versions of every check shipped with the agent
  # Used by the `datadog-agent integration` command to prevent downgrading a check to a version
  # older than the one shipped in the agent
  copy "#{project_dir}/requirements-agent-release.txt", "#{install_dir}/"
end
