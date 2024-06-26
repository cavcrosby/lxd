# LXD-related test helpers.

spawn_lxd() {
    set +x
    # LXD_DIR is local here because since $(lxc) is actually a function, it
    # overwrites the environment and we would lose LXD_DIR's value otherwise.

    # shellcheck disable=2039
    local LXD_DIR lxddir lxd_backend

    lxddir=${1}
    shift

    # shellcheck disable=SC2153
    if [ "$LXD_BACKEND" = "random" ]; then
        lxd_backend="$(random_storage_backend)"
    else
        lxd_backend="$LXD_BACKEND"
    fi

    # Copy pre generated Certs
    cp deps/server.crt "${lxddir}"
    cp deps/server.key "${lxddir}"

    # setup storage
    "$lxd_backend"_setup "${lxddir}"
    echo "$lxd_backend" > "${lxddir}/lxd.backend"

    echo "==> Spawning lxd in ${lxddir}"
    # shellcheck disable=SC2086
    LXD_DIR="${lxddir}" lxd --logfile "${lxddir}/lxd.log" ${DEBUG-} "$@" 2>&1 &
    LXD_PID=$!
    echo "${LXD_PID}" > "${lxddir}/lxd.pid"
    # shellcheck disable=SC2153
    echo "${lxddir}" >> "${TEST_DIR}/daemons"
    echo "==> Spawned LXD (PID is ${LXD_PID})"

    echo "==> Confirming lxd is responsive"
    LXD_DIR="${lxddir}" lxd waitready --timeout=300

    echo "==> Binding to network"
    # shellcheck disable=SC2034
    for i in $(seq 10); do
        addr="127.0.0.1:$(local_tcp_port)"
        LXD_DIR="${lxddir}" lxc config set core.https_address "${addr}" || continue
        echo "${addr}" > "${lxddir}/lxd.addr"
        echo "==> Bound to ${addr}"
        break
    done

    echo "==> Setting trust password"
    LXD_DIR="${lxddir}" lxc config set core.trust_password foo
    if [ -n "${DEBUG:-}" ]; then
        set -x
    fi

    echo "==> Configuring storage backend"
    "$lxd_backend"_configure "${lxddir}"
}

respawn_lxd() {
    set +x
    # LXD_DIR is local here because since $(lxc) is actually a function, it
    # overwrites the environment and we would lose LXD_DIR's value otherwise.

    # shellcheck disable=2039
    local LXD_DIR

    lxddir=${1}
    shift

    echo "==> Spawning lxd in ${lxddir}"
    # shellcheck disable=SC2086
    LXD_DIR="${lxddir}" lxd --logfile "${lxddir}/lxd.log" ${DEBUG-} "$@" 2>&1 &
    LXD_PID=$!
    echo "${LXD_PID}" > "${lxddir}/lxd.pid"
    echo "==> Spawned LXD (PID is ${LXD_PID})"

    echo "==> Confirming lxd is responsive"
    LXD_DIR="${lxddir}" lxd waitready --timeout=300
}

kill_lxd() {
    # LXD_DIR is local here because since $(lxc) is actually a function, it
    # overwrites the environment and we would lose LXD_DIR's value otherwise.

    # shellcheck disable=2039
    local LXD_DIR daemon_dir daemon_pid check_leftovers lxd_backend

    daemon_dir=${1}
    LXD_DIR=${daemon_dir}
    daemon_pid=$(cat "${daemon_dir}/lxd.pid")
    check_leftovers="false"
    lxd_backend=$(storage_backend "$daemon_dir")
    echo "==> Killing LXD at ${daemon_dir}"

    if [ -e "${daemon_dir}/unix.socket" ]; then
        # Delete all containers
        echo "==> Deleting all containers"
        for container in $(lxc list --fast --force-local | tail -n+3 | grep "^| " | cut -d' ' -f2); do
            lxc delete "${container}" --force-local -f || true
        done

        # Delete all images
        echo "==> Deleting all images"
        for image in $(lxc image list --force-local | tail -n+3 | grep "^| " | cut -d'|' -f3 | sed "s/^ //g"); do
            lxc image delete "${image}" --force-local || true
        done

        # Delete all profiles
        echo "==> Deleting all profiles"
        for profile in $(lxc profile list --force-local); do
            lxc profile delete "${profile}" --force-local || true
        done

        echo "==> Checking for locked DB tables"
        for table in $(echo .tables | sqlite3 "${daemon_dir}/lxd.db"); do
            echo "SELECT * FROM ${table};" | sqlite3 "${daemon_dir}/lxd.db" >/dev/null
        done

        # Kill the daemon
        lxd shutdown || kill -9 "${daemon_pid}" 2>/dev/null || true

        # Cleanup shmounts (needed due to the forceful kill)
        find "${daemon_dir}" -name shmounts -exec "umount" "-l" "{}" \; >/dev/null 2>&1 || true
        find "${daemon_dir}" -name devlxd -exec "umount" "-l" "{}" \; >/dev/null 2>&1 || true

        check_leftovers="true"
    fi

    if [ -n "${LXD_LOGS:-}" ]; then
        echo "==> Copying the logs"
        mkdir -p "${LXD_LOGS}/${daemon_pid}"
        cp -R "${daemon_dir}/logs/" "${LXD_LOGS}/${daemon_pid}/"
        cp "${daemon_dir}/lxd.log" "${LXD_LOGS}/${daemon_pid}/"
    fi

    if [ "${check_leftovers}" = "true" ]; then
        echo "==> Checking for leftover files"
        rm -f "${daemon_dir}/containers/lxc-monitord.log"
        rm -f "${daemon_dir}/security/apparmor/cache/.features"
        check_empty "${daemon_dir}/containers/"
        check_empty "${daemon_dir}/devices/"
        check_empty "${daemon_dir}/images/"
        # FIXME: Once container logging rework is done, uncomment
        # check_empty "${daemon_dir}/logs/"
        check_empty "${daemon_dir}/security/apparmor/cache/"
        check_empty "${daemon_dir}/security/apparmor/profiles/"
        check_empty "${daemon_dir}/security/seccomp/"
        check_empty "${daemon_dir}/shmounts/"
        check_empty "${daemon_dir}/snapshots/"

        echo "==> Checking for leftover DB entries"
        check_empty_table "${daemon_dir}/lxd.db" "containers"
        check_empty_table "${daemon_dir}/lxd.db" "containers_config"
        check_empty_table "${daemon_dir}/lxd.db" "containers_devices"
        check_empty_table "${daemon_dir}/lxd.db" "containers_devices_config"
        check_empty_table "${daemon_dir}/lxd.db" "containers_profiles"
        check_empty_table "${daemon_dir}/lxd.db" "images"
        check_empty_table "${daemon_dir}/lxd.db" "images_aliases"
        check_empty_table "${daemon_dir}/lxd.db" "images_properties"
        check_empty_table "${daemon_dir}/lxd.db" "images_source"
        check_empty_table "${daemon_dir}/lxd.db" "profiles"
        check_empty_table "${daemon_dir}/lxd.db" "profiles_config"
        check_empty_table "${daemon_dir}/lxd.db" "profiles_devices"
        check_empty_table "${daemon_dir}/lxd.db" "profiles_devices_config"
    fi

    # teardown storage
    "$lxd_backend"_teardown "${daemon_dir}"

    # Wipe the daemon directory
    wipe "${daemon_dir}"

    # Remove the daemon from the list
    sed "\\|^${daemon_dir}|d" -i "${TEST_DIR}/daemons"
}

shutdown_lxd() {
    # LXD_DIR is local here because since $(lxc) is actually a function, it
    # overwrites the environment and we would lose LXD_DIR's value otherwise.

    # shellcheck disable=2039
    local LXD_DIR

    daemon_dir=${1}
    # shellcheck disable=2034
    LXD_DIR=${daemon_dir}
    daemon_pid=$(cat "${daemon_dir}/lxd.pid")
    echo "==> Killing LXD at ${daemon_dir}"

    # Kill the daemon
    lxd shutdown || kill -9 "${daemon_pid}" 2>/dev/null || true
}

wait_for() {
    # shellcheck disable=SC2039
    local addr op

    addr=${1}
    shift
    op=$("$@" | jq -r .operation)
    my_curl "https://${addr}${op}/wait"
}

wipe() {
    if which btrfs >/dev/null 2>&1; then
        rm -Rf "${1}" 2>/dev/null || true
        if [ -d "${1}" ]; then
            find "${1}" | tac | xargs btrfs subvolume delete >/dev/null 2>&1 || true
        fi
    fi

    # shellcheck disable=SC2039
    local pid
    # shellcheck disable=SC2009
    ps aux | grep lxc-monitord | grep "${1}" | awk '{print $2}' | while read -r pid; do
        kill -9 "${pid}" || true
    done

    if mountpoint -q "${1}"; then
        umount "${1}"
    fi

    rm -Rf "${1}"
}

# Kill and cleanup LXD instances and related resources
cleanup_lxds() {
    # shellcheck disable=SC2039
    local test_dir daemon_dir
    test_dir="$1"

    # Kill all LXD instances
    while read -r daemon_dir; do
        kill_lxd "${daemon_dir}"
    done < "${test_dir}/daemons"

    # Wipe the test environment
    wipe "$test_dir"

    umount_loops "$test_dir"
}
