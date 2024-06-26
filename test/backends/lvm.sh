lvm_setup() {
  # shellcheck disable=2039
  local LXD_DIR

  LXD_DIR=$1

  echo "==> Setting up lvm backend in ${LXD_DIR}"

  truncate -s 4G "${TEST_DIR}/$(basename "${LXD_DIR}").lvm"
  pvloopdev=$(losetup --show -f "${TEST_DIR}/$(basename "${LXD_DIR}").lvm")
  if [ ! -e "${pvloopdev}" ]; then
    echo "failed to setup loop"
    false
  fi
  echo "${pvloopdev}" > "${TEST_DIR}/$(basename "${LXD_DIR}").lvm.vg"

  pvcreate "${pvloopdev}"
  vgcreate "lxdtest-$(basename "${LXD_DIR}")" "${pvloopdev}"
}

lvm_configure() {
  # shellcheck disable=2039
  local LXD_DIR

  LXD_DIR=$1

  echo "==> Configuring lvm backend in ${LXD_DIR}"

  lxc config set storage.lvm_volume_size "50Mib"
  lxc config set storage.lvm_vg_name "lxdtest-$(basename "${LXD_DIR}")"
}

lvm_teardown() {
  # shellcheck disable=2039
  local LXD_DIR

  LXD_DIR=$1

  echo "==> Tearing down lvm backend in ${LXD_DIR}"

  SUCCESS=0
  # shellcheck disable=SC2034
  for i in $(seq 10); do
    vgremove -f "lxdtest-$(basename "${LXD_DIR}")" >/dev/null 2>&1 || true
    pvremove -f "$(cat "${TEST_DIR}/$(basename "${LXD_DIR}").lvm.vg")" >/dev/null 2>&1 || true
    if losetup -d "$(cat "${TEST_DIR}/$(basename "${LXD_DIR}").lvm.vg")"; then
      SUCCESS=1
      break
    fi

    sleep 0.5
  done

  if [ "${SUCCESS}" = "0" ]; then
    echo "Failed to tear down LVM"
    false
  fi

  rm -f "${TEST_DIR}/$(basename "${LXD_DIR}").lvm"
  rm -f "${TEST_DIR}/$(basename "${LXD_DIR}").lvm.vg"
}
