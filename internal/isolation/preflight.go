package isolation

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// CheckStatus describes only the particular claim in a Check, not containment.
type CheckStatus string

const (
	Verified    CheckStatus = "verified"
	Unavailable CheckStatus = "unavailable"
	Unknown     CheckStatus = "unknown"
)

type Check struct {
	Control string      `json:"control"`
	Status  CheckStatus `json:"status"`
	Detail  string      `json:"detail"`
}
type PreflightReport struct {
	Ready             bool     `json:"ready"`
	IsolationVerified bool     `json:"isolation_verified"`
	Checks            []Check  `json:"checks"`
	Limitations       []string `json:"limitations"`
}

// HostProbe is read-only discovery, injectable for tests. It must never execute
// discovered binaries, create namespaces, contact sockets or mutate the host.
// ReadFile must be bounded. Paths are fixed by InspectHost, not task input.
type HostProbe interface {
	GOOS() string
	ReadFile(string) ([]byte, error)
	Executable(string) (bool, error)
}
type localProbe struct{}

func (localProbe) GOOS() string { return runtime.GOOS }
func (localProbe) ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if len(b) > 64*1024 {
		return nil, errors.New("probe data exceeds limit")
	}
	return b, err
}
func (localProbe) Executable(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0, nil
}

// Preflight performs passive discovery on the current host. It does not inspect
// any daemon secret, database, credential, environment or user home directory.
func Preflight() PreflightReport { return InspectHost(localProbe{}) }

// InspectHost never certifies isolation: a production runtime adapter and live
// containment tests do not exist yet. A verified discovery check proves only
// that a prerequisite is visible, not that the runner can use it effectively.
func InspectHost(p HostProbe) PreflightReport {
	r := PreflightReport{Limitations: []string{
		"Passive discovery only; no binaries executed, containers started, namespaces created, sockets contacted, or host settings changed.",
		"Verified means only the stated presence/configuration was observed; it does not certify effective worker isolation.",
		"Fixed runtime locations only; custom installations may be missed. The current process may see a restricted host view.",
		"No production runtime adapter or model gateway exists. Public execution remains disabled regardless of this report.",
	}}
	add := func(name string, status CheckStatus, detail string) {
		r.Checks = append(r.Checks, Check{name, status, detail})
	}
	if p == nil {
		add("host_probe", Unknown, "No read-only host probe supplied.")
	} else if p.GOOS() != "linux" {
		add("linux_host", Unavailable, "The required Linux host is not visible to this process.")
	} else {
		add("linux_host", Verified, "Current process reports Linux; kernel capabilities are not certified.")
		found, uncertain := false, false
		for _, name := range []string{"podman", "docker", "runc"} {
			present, unknown := false, false
			for _, dir := range []string{"/usr/bin/", "/usr/local/bin/", "/bin/"} {
				ok, err := p.Executable(dir + name)
				if err != nil {
					if !errors.Is(err, fs.ErrNotExist) {
						unknown = true
					}
					continue
				}
				present = present || ok
			}
			status, detail := Unavailable, "No executable found in the fixed discovery locations."
			if present {
				status = Verified
				detail = "Executable metadata found; version, provenance, rootless operation and usability are unverified."
				found = true
			} else if unknown {
				status = Unknown
				detail = "Executable metadata could not be inspected in at least one location."
				uncertain = true
			}
			add("binary_"+name, status, detail)
		}
		if found {
			add("rootless_runtime", Unknown, "Binary presence is insufficient to verify a usable rootless runtime.")
		} else if uncertain {
			add("rootless_runtime", Unknown, "No usable runtime established; some discovery locations were inaccessible.")
		} else {
			add("rootless_runtime", Unavailable, "No candidate runtime found in the fixed discovery locations; custom locations were not searched.")
		}
		b, err := p.ReadFile("/sys/fs/cgroup/cgroup.controllers")
		if err != nil {
			add("cgroup_v2_controllers", readStatus(err), "Cannot inspect the required cgroup v2 controllers file.")
		} else {
			present := map[string]bool{}
			for _, v := range strings.Fields(string(b)) {
				present[v] = true
			}
			missing := []string{}
			for _, v := range []string{"cpu", "memory", "pids"} {
				if !present[v] {
					missing = append(missing, v)
				}
			}
			if len(missing) > 0 {
				add("cgroup_v2_controllers", Unavailable, "Required visible controllers missing: "+strings.Join(missing, ", "))
			} else {
				add("cgroup_v2_controllers", Verified, "cpu, memory and pids are visible; runner delegation and effective limits remain unverified.")
			}
		}
		b, err = p.ReadFile("/proc/sys/user/max_user_namespaces")
		if err != nil {
			add("user_namespace_configuration", readStatus(err), "Cannot inspect user namespace sysctl.")
		} else if n, e := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); e != nil {
			add("user_namespace_configuration", Unknown, "User namespace sysctl has an unrecognized value.")
		} else if n == 0 {
			add("user_namespace_configuration", Unavailable, "User namespace sysctl is zero.")
		} else {
			add("user_namespace_configuration", Verified, "User namespace limit is nonzero; policy restrictions and actual creation remain unverified.")
		}
		b, err = p.ReadFile("/sys/kernel/security/lsm")
		if err != nil {
			add("host_lsm_configuration", readStatus(err), "Cannot inspect the active LSM list; securityfs may be hidden or unmounted.")
		} else {
			present := false
			for _, v := range strings.Split(strings.TrimSpace(string(b)), ",") {
				if v == "apparmor" || v == "selinux" {
					present = true
				}
			}
			if present {
				add("host_lsm_configuration", Verified, "AppArmor/SELinux listed; enforcing mode and applied worker policy remain unverified.")
			} else {
				add("host_lsm_configuration", Unavailable, "No required AppArmor or SELinux entry visible in the active LSM list.")
			}
		}
	}
	// Explicitly enumerate every remaining enforcement category. Passive host
	// discovery must never promote these to verified on the strength of flags.
	for _, control := range []string{
		"distinct_runner_identity_and_daemon_storage", "subordinate_uid_gid_mapping",
		"private_mount_pid_ipc_network_namespaces", "nonroot_container_identity",
		"readonly_root_and_task_only_storage", "no_host_sockets_or_devices",
		"clean_environment_and_descriptors", "capabilities_and_no_new_privileges",
		"effective_seccomp_and_lsm", "effective_cpu_memory_pid_limits",
		"workspace_and_scratch_disk_quotas", "network_egress_denial",
		"reviewed_image_digest", "authenticated_runner_transport",
		"bounded_output_and_concurrency", "wall_clock_and_tree_termination",
		"capability_revocation_and_restart_cleanup", "adversarial_containment_tests",
	} {
		add(control, Unknown, "Requires trusted runner configuration and effective sandbox verification; not established by passive discovery.")
	}
	add("production_runtime_adapter", Unavailable, "No production adapter is implemented; fake-runtime tests cannot certify isolation.")
	add("model_gateway_and_approval_broker", Unavailable, "No isolated worker gateway/approval transport is implemented; Codex/Claude execution is not enabled.")
	return r
}
func readStatus(err error) CheckStatus {
	if errors.Is(err, fs.ErrNotExist) {
		return Unavailable
	}
	return Unknown
}

// RequireReady makes non-readiness an explicit error for diagnostic callers.
// It is not an authorization mechanism or a replacement for runtime evidence.
func (r PreflightReport) RequireReady() error {
	if !r.Ready || !r.IsolationVerified {
		return fmt.Errorf("isolated execution unavailable: preflight does not verify containment")
	}
	return nil
}
