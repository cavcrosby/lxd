test_migration() {
  # setup a second LXD
  # shellcheck disable=2039
  local LXD2_DIR LXD2_ADDR lxd_backend

  # shellcheck disable=2153
  lxd_backend=$(storage_backend "$LXD_DIR")

  LXD2_DIR=$(mktemp -d -p "${TEST_DIR}" XXX)
  chmod +x "${LXD2_DIR}"
  spawn_lxd "${LXD2_DIR}"
  LXD2_ADDR=$(cat "${LXD2_DIR}/lxd.addr")

  # shellcheck disable=2153
  lxd2_backend=$(storage_backend "$LXD2_DIR")

  ensure_import_testimage

  if ! lxc_remote remote list | grep -q l1; then
    # shellcheck disable=2153
    lxc_remote remote add l1 "${LXD_ADDR}" --accept-certificate --password foo
  fi
  if ! lxc_remote remote list | grep -q l2; then
    lxc_remote remote add l2 "${LXD2_ADDR}" --accept-certificate --password foo
  fi

  lxc_remote init testimage nonlive
  # test moving snapshots
  lxc_remote config set l1:nonlive user.tester foo
  lxc_remote snapshot l1:nonlive
  lxc_remote config unset l1:nonlive user.tester
  lxc_remote move l1:nonlive l2:
  lxc_remote config show l2:nonlive/snap0 | grep user.tester | grep foo

  # This line exists so that the container's storage volume is mounted when we
  # perform existence check for various files.
  lxc_remote start l2:nonlive
  # FIXME: make this backend agnostic
  if [ "${lxd2_backend}" != "lvm" ]; then
    [ -d "${LXD2_DIR}/containers/nonlive/rootfs" ]
  fi
  lxc_remote stop l2:nonlive

  [ ! -d "${LXD_DIR}/containers/nonlive" ]
  # FIXME: make this backend agnostic
  if [ "${lxd2_backend}" = "dir" ]; then
    [ -d "${LXD2_DIR}/snapshots/nonlive/snap0/rootfs/bin" ]
  fi

  lxc_remote copy l2:nonlive l1:nonlive2
  [ -d "${LXD_DIR}/containers/nonlive2" ]
  # FIXME: make this backend agnostic
  if [ "${lxd2_backend}" != "lvm" ]; then
    [ -d "${LXD2_DIR}/containers/nonlive/rootfs/bin" ]
  fi
  # FIXME: make this backend agnostic
  if [ "${lxd_backend}" = "dir" ]; then
    [ -d "${LXD_DIR}/snapshots/nonlive2/snap0/rootfs/bin" ]
  fi

  lxc_remote copy l1:nonlive2/snap0 l2:nonlive3
  # FIXME: make this backend agnostic
  if [ "${lxd2_backend}" != "lvm" ]; then
    [ -d "${LXD2_DIR}/containers/nonlive3/rootfs/bin" ]
  fi
  lxc_remote delete l2:nonlive3 --force

  lxc_remote copy l2:nonlive l2:nonlive2
  # should have the same base image tag
  [ "$(lxc_remote config get l2:nonlive volatile.base_image)" = "$(lxc_remote config get l2:nonlive2 volatile.base_image)" ]
  # check that nonlive2 has a new addr in volatile
  [ "$(lxc_remote config get l2:nonlive volatile.eth0.hwaddr)" != "$(lxc_remote config get l2:nonlive2 volatile.eth0.hwaddr)" ]

  lxc_remote config unset l2:nonlive volatile.base_image
  lxc_remote copy l2:nonlive l1:nobase
  lxc_remote delete l1:nobase

  lxc_remote start l1:nonlive2
  lxc_remote list l1: | grep RUNNING | grep nonlive2
  lxc_remote delete l1:nonlive2 l2:nonlive2 --force

  lxc_remote start l2:nonlive
  lxc_remote list l2: | grep RUNNING | grep nonlive
  lxc_remote delete l2:nonlive --force

  if ! which criu >/dev/null 2>&1; then
    kill_lxd "$LXD2_DIR"
    echo "==> SKIP: live migration with CRIU (missing binary)"
    return
  fi

  lxc_remote launch testimage l1:migratee

  # let the container do some interesting things
  sleep 1

  lxc_remote stop --stateful l1:migratee
  lxc_remote start l1:migratee
  lxc_remote delete --force l1:migratee

  kill_lxd "$LXD2_DIR"
}
