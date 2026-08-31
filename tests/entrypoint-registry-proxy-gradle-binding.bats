#!/usr/bin/env bats
# Gradle Binding for the registry proxy (ADR 0044, issue #2858):
# phase_gradle_binding writes a Gradle init script under
# $GRADLE_USER_HOME/init.d/ once phase_registry_proxy_bindings's Forwarder
# is confirmed up (the _registry_proxy_forwarder_ready sentinel), pointing
# buildscript, plugin-management, dependency-resolution-management, and
# per-project repositories at the local Forwarder. Unlike cargo's binding
# (spawn/readiness/rendering now covered by
# cmd/launcher/internal/bindregistry/forwarder_test.go and
# cmd/launcher/driver-exec/bindregistry_cmd_test.go, issue #2931), this needs
# no in-tree rewrite: Gradle's init.d directory is a home-level config
# mechanism outside the cloned repo, the same way $CARGO_HOME/config.toml is.
#
# REGISTRY_PROXY_FORWARDER_PORT=27185 here is deliberately distinct from the
# cargo suite's 27184 (entrypoint-cargo-intree-binding.bats) so a socat
# TCP-LISTEN bind never collides if bats sharding runs multiple *.bats files
# concurrently. The socat-off-PATH and readiness-poll-timeout tests below use
# 27186/27187 for the same reason, distinct from every other port already in
# use in this file and its siblings.
#
# This suite only exercises phase_gradle_binding's own control flow -- the
# init script it writes, and when -- via bats/socat fakes; it has no
# JDK/gradle dependency and does not invoke gradle itself. The Groovy
# mechanism the generated init script relies on (which lifecycle hook wins
# for buildscript vs. settings vs. project repositories, the persistent
# repos.all{} listener that closes the append-after-clear escape a one-shot
# clear-then-add has wherever a competing declaration can still run after
# installation, the Gradle <6.0 version guards, and the FAIL_ON_PROJECT_REPOS
# interaction) was verified interactively against a real Gradle 8.14.4,
# outside this repo's own toolchain (see ADR 0044's Consequences section and
# phase_gradle_binding's own doc comment in agent/entrypoint.sh).

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  kill_stand_in_socat
  # The socat-off-PATH/timeout tests below each start a stub `socat` that
  # survives its own `run bash "$ENTRYPOINT"`
  # call detached, the same way the real Forwarder does in production (see
  # SpawnSocat's own comment on fd-closing in
  # cmd/launcher/internal/bindregistry/forwarder.go) -- clean it up here too,
  # alongside kill_stand_in_socat's real stand-in socat.
  if [ -n "${_stub_socat_pidfile:-}" ] && [ -f "$_stub_socat_pidfile" ]; then
    kill "$(cat "$_stub_socat_pidfile")" 2>/dev/null
  fi
  true
}

@test "socket present: Forwarder starts and gradle init script is written (issue #2858)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!

  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local _init_script="$HOME/.gradle/init.d/spindrift-registry-proxy.init.gradle"
  [ -f "$_init_script" ]
  grep -qF "http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/\"" "$_init_script"
  # No path segment beyond the Forwarder's own root: REGISTRY_PROXY_UPSTREAM_URL
  # already carries whatever registry-specific base path the operator's
  # upstream needs.
  ! grep -q "maven2" "$_init_script"
  grep -q "allowInsecureProtocol" "$_init_script"
  grep -q "buildscript.repositories" "$_init_script"
  grep -q "pluginManagement.repositories" "$_init_script"
  grep -q "dependencyResolutionManagement.repositories" "$_init_script"
  grep -q "projectsEvaluated" "$_init_script"
  grep -q "settingsEvaluated" "$_init_script"
  # beforeSettings fires before the settings script body runs, so it's the
  # only hook early enough to cover a settings-level plugins{} block and a
  # settings.buildscript{} block.
  grep -q "beforeSettings" "$_init_script"
  grep -q "settings.buildscript.repositories" "$_init_script"
  # spindriftPersistentRedirect (backed by a repos.all{} listener) is needed
  # because a one-shot clear-then-add is escaped by
  # any repository a project's own buildscript{}/repositories{} block, or a
  # settings script's own pluginManagement{repositories{}} block, appends
  # *after* the clear runs. The listener keeps removing every later
  # addition, so it's used at the three call sites early enough for that to
  # happen: top-level buildscript.repositories, and beforeSettings'
  # settings.pluginManagement.repositories/settings.buildscript.repositories.
  grep -q "spindriftPersistentRedirect(buildscript.repositories)" "$_init_script"
  grep -q "spindriftPersistentRedirect(settings.pluginManagement.repositories)" "$_init_script"
  grep -q "spindriftPersistentRedirect(settings.buildscript.repositories)" "$_init_script"
  grep -q "repos.all" "$_init_script"
  grep -qF "name = 'spindrift'" "$_init_script"
  # The settingsEvaluated/projectsEvaluated call sites fire only after the
  # thing they compete with has already finished declaring repositories, so
  # a plain one-shot clear-then-add (spindriftRedirect) is still sufficient
  # there -- nothing can append behind it.
  #
  # settingsEvaluated's pluginManagement.repositories re-apply is the
  # Gradle <6.0 fallback ONLY: on 6.0+, beforeSettings' persistent listener
  # (spindriftPersistentRedirect) is already attached to that same
  # container, so re-running the one-shot form here would clear it, add an
  # unnamed repo the listener immediately strips (its name never became
  # 'spindrift'), and leave pluginManagement.repositories empty. Guarding
  # this call on spindriftPluginManagementManaged (set true once
  # beforeSettings' persistent redirects succeed) is the fix for that
  # regression -- assert the guard wraps the
  # call directly, not just that both strings appear somewhere in the file.
  grep -q "spindriftPluginManagementManaged" "$_init_script"
  grep -A1 "if (!spindriftPluginManagementManaged)" "$_init_script" | grep -q "spindriftRedirect(settings.pluginManagement.repositories)"
  grep -q "spindriftRedirect(settings.dependencyResolutionManagement.repositories)" "$_init_script"
  grep -q "spindriftRedirect(repositories)" "$_init_script"
  # gradle.beforeSettings and allowInsecureProtocol both require Gradle
  # 6.0+; both unguarded uses would throw and kill every build on older
  # Gradle.
  grep -q "catch (MissingMethodException" "$_init_script"
  grep -q "catch (MissingPropertyException" "$_init_script"

  local _gradle_line
  _gradle_line="$(echo "$output" | grep "gradle bound to the registry proxy Forwarder")"
  [[ "$_gradle_line" == *"$_init_script"* ]]

  # Same happy path also renders cargo's user-level binding and exports the
  # npm/pnpm/yarn berry env overrides (see NpmFamilyBindings/CargoConfigTOML,
  # cmd/launcher/internal/bindregistry/registrybindings.go) -- this suite is
  # the surviving home for that assertion since the deleted
  # entrypoint-registry-proxy-forwarder.bats (issue #2931) covered it, and
  # this test already stands up a real Forwarder. $DRIVER_LOG carries what
  # the fake Driver process (tests/fakes/_driver-common.bash) actually
  # inherited, proving the env vars reach a child process, not just the
  # entrypoint shell that exported them.
  [ -f "$HOME/.cargo/config.toml" ]
  grep -q 'replace-with = "spindrift-registry-proxy"' "$HOME/.cargo/config.toml"
  grep -q "registry = \"sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/\"" "$HOME/.cargo/config.toml"

  grep -q "env: npm_config_registry=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/" "$DRIVER_LOG"
  grep -q "env: pnpm_config_registry=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/" "$DRIVER_LOG"
  grep -q "env: YARN_NPM_REGISTRY_SERVER=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/" "$DRIVER_LOG"
}

@test "socket present: gradle init script honors a custom GRADLE_USER_HOME (issue #2858)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185
  export GRADLE_USER_HOME="$BATS_TEST_TMPDIR/custom-gradle-home"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!

  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local _init_script="$GRADLE_USER_HOME/init.d/spindrift-registry-proxy.init.gradle"
  [ -f "$_init_script" ]
  [ ! -f "$HOME/.gradle/init.d/spindrift-registry-proxy.init.gradle" ]

  local _gradle_line
  _gradle_line="$(echo "$output" | grep "gradle bound to the registry proxy Forwarder")"
  [[ "$_gradle_line" == *"$_init_script"* ]]
}

@test "socket absent: phase is a silent no-op (issue #2858)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$HOME/.gradle/init.d/spindrift-registry-proxy.init.gradle" ]

  ! echo "$output" | grep -q "gradle bound to the registry proxy Forwarder"
  [[ "$output" != *"==> WARNING:"* ]]
}

@test "socket absent, ambient FORWARDER_READY leaked from outside the phase: still a silent no-op (issue #2931)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  # Simulates a pre-existing FORWARDER_READY sitting in the ambient
  # environment from outside phase_registry_proxy_bindings's control (e.g. a
  # leaked export from a parent process or a prior phase) -- exactly what
  # phase_registry_proxy_bindings's `unset FORWARDER_READY` right before
  # invoking `driver-exec bind-registry` (agent/entrypoint.sh) guards
  # against. The socket path above doesn't exist, so the verb is a silent
  # no-op that writes nothing to its bindings-env-output file; without the
  # guard, the read at `_registry_proxy_forwarder_ready="${FORWARDER_READY:-}"`
  # would pick up this ambient value instead and wrongly conclude the
  # Forwarder is up.
  export FORWARDER_READY=1

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$HOME/.gradle/init.d/spindrift-registry-proxy.init.gradle" ]
  # Not just gradle: the same ambient leak would also fool the npm/yarn/pnpm
  # in-tree binding phases, which share the same _registry_proxy_forwarder_ready
  # sentinel. $HOME/.cargo/config.toml is the user-level cargo binding the
  # verb itself renders directly to disk -- asserting it too catches a
  # regression in the no-op path as a whole, not just the gradle phase.
  [ ! -f "$HOME/.cargo/config.toml" ]

  ! echo "$output" | grep -q "gradle bound to the registry proxy Forwarder"
}

@test "socket present, socat off PATH: Forwarder never starts, gradle init script never written (issue #2858)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27186

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!

  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  # Can't literally uninstall socat -- the setup above needs a real socat on
  # PATH to create the stand-in socket. Instead, drop every PATH directory
  # that holds a socat binary, not just the one `command -v socat` resolves
  # first: this sandbox's own PATH carries socat in more than one directory
  # (e.g. a nix store path and /bin), so stripping only the first would leave
  # phase_registry_proxy_bindings' own `command -v socat` guard still
  # resolving it via the second, and the Forwarder would actually start.
  # IFS=: is scoped to this command substitution's own subshell, not the
  # test body, so it never leaks into the `run bash "$ENTRYPOINT"` call below.
  local _trimmed_path
  _trimmed_path="$(
    IFS=:
    local _out="" _p
    for _p in $PATH; do
      [ -x "$_p/socat" ] && continue
      _out="${_out:+$_out:}$_p"
    done
    printf '%s' "$_out"
  )"

  local _orig_path="$PATH"
  PATH="$_trimmed_path"
  run bash "$ENTRYPOINT"
  PATH="$_orig_path"
  [ "$status" -eq 0 ]

  [[ "$output" == *"WARNING: $REGISTRY_PROXY_SOCKET_PATH is mounted but socat is not on PATH"* ]]
  [ ! -f "$HOME/.gradle/init.d/spindrift-registry-proxy.init.gradle" ]
  ! echo "$output" | grep -q "gradle bound to the registry proxy Forwarder"
}

@test "socket present, Forwarder readiness poll times out: gradle init script never written (issue #2858)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27187

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!

  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  # A stub `socat` ahead of the real one on PATH: `command -v socat` still
  # finds it, so phase_registry_proxy_bindings proceeds past its
  # not-on-PATH check, but the stub never binds the TCP port, so the real
  # 50 x 0.1s (~5s) readiness poll genuinely exhausts -- this test takes
  # ~5s of real wall-clock time, matching production's own timeout.
  local _stub_bin="$BATS_TEST_TMPDIR/stub-bin"
  mkdir -p "$_stub_bin"
  _stub_socat_pidfile="$BATS_TEST_TMPDIR/stub-socat.pid"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<EOF
echo \$\$ >"$_stub_socat_pidfile"
exec sleep 60
EOF
  } >"$_stub_bin/socat"
  chmod +x "$_stub_bin/socat"

  local _orig_path="$PATH"
  PATH="$_stub_bin:$PATH"
  run bash "$ENTRYPOINT"
  PATH="$_orig_path"
  [ "$status" -eq 0 ]

  [[ "$output" == *"WARNING: registry proxy Forwarder did not start listening on 127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}"* ]]
  [ ! -f "$HOME/.gradle/init.d/spindrift-registry-proxy.init.gradle" ]
  ! echo "$output" | grep -q "gradle bound to the registry proxy Forwarder"
}
