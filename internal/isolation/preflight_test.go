package isolation

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

type probeFixture struct {
	goos        string
	files       map[string]string
	executables map[string]bool
	errs        map[string]error
	reads       []string
}

func (p *probeFixture) GOOS() string { return p.goos }
func (p *probeFixture) ReadFile(path string) ([]byte, error) {
	p.reads = append(p.reads, path)
	if err := p.errs[path]; err != nil {
		return nil, err
	}
	if b, ok := p.files[path]; ok {
		return []byte(b), nil
	}
	return nil, fs.ErrNotExist
}
func (p *probeFixture) Executable(path string) (bool, error) {
	p.reads = append(p.reads, path)
	if err := p.errs[path]; err != nil {
		return false, err
	}
	if v, ok := p.executables[path]; ok {
		return v, nil
	}
	return false, fs.ErrNotExist
}
func baselineProbe() *probeFixture {
	return &probeFixture{goos: "linux", files: map[string]string{"/sys/fs/cgroup/cgroup.controllers": "cpu memory pids io", "/proc/sys/user/max_user_namespaces": "15000\n", "/sys/kernel/security/lsm": "capability,landlock,apparmor\n"}, executables: map[string]bool{"/usr/bin/podman": true}, errs: map[string]error{}}
}
func findCheck(t *testing.T, r PreflightReport, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Control == name {
			return c
		}
	}
	t.Fatalf("missing control %s", name)
	return Check{}
}
func TestPreflightNeverCertifiesContainment(t *testing.T) {
	p := baselineProbe()
	r := InspectHost(p)
	if r.Ready || r.IsolationVerified || r.RequireReady() == nil {
		t.Fatal("passive discovery certified isolation")
	}
	for _, name := range []string{"linux_host", "binary_podman", "cgroup_v2_controllers", "user_namespace_configuration", "host_lsm_configuration"} {
		if c := findCheck(t, r, name); c.Status != Verified {
			t.Fatal(c)
		}
	}
	for _, name := range []string{"rootless_runtime", "distinct_runner_identity_and_daemon_storage", "effective_cpu_memory_pid_limits", "network_egress_denial", "adversarial_containment_tests"} {
		if c := findCheck(t, r, name); c.Status != Unknown {
			t.Fatal(c)
		}
	}
	if findCheck(t, r, "production_runtime_adapter").Status != Unavailable {
		t.Fatal("missing adapter not reported")
	}
	allowed := map[string]bool{"/sys/fs/cgroup/cgroup.controllers": true, "/proc/sys/user/max_user_namespaces": true, "/sys/kernel/security/lsm": true}
	for _, dir := range []string{"/usr/bin/", "/usr/local/bin/", "/bin/"} {
		for _, name := range []string{"podman", "docker", "runc"} {
			allowed[dir+name] = true
		}
	}
	for _, path := range p.reads {
		if !allowed[path] {
			t.Fatal("unexpected probe path", path)
		}
	}
}
func TestPreflightMissingAndUnknown(t *testing.T) {
	p := &probeFixture{goos: "linux"}
	r := InspectHost(p)
	for _, name := range []string{"rootless_runtime", "cgroup_v2_controllers", "user_namespace_configuration", "host_lsm_configuration"} {
		if c := findCheck(t, r, name); c.Status != Unavailable {
			t.Fatal(c)
		}
	}
	p = baselineProbe()
	p.executables = nil
	p.errs["/usr/bin/podman"] = fs.ErrPermission
	p.errs["/sys/fs/cgroup/cgroup.controllers"] = fs.ErrPermission
	p.errs["/proc/sys/user/max_user_namespaces"] = errors.New("probe read error")
	p.errs["/sys/kernel/security/lsm"] = fs.ErrPermission
	r = InspectHost(p)
	for _, name := range []string{"rootless_runtime", "binary_podman", "cgroup_v2_controllers", "user_namespace_configuration", "host_lsm_configuration"} {
		if c := findCheck(t, r, name); c.Status != Unknown {
			t.Fatal(c)
		}
	}
	for _, test := range []struct {
		path, value, control string
		status               CheckStatus
	}{
		{"/proc/sys/user/max_user_namespaces", "0", "user_namespace_configuration", Unavailable},
		{"/proc/sys/user/max_user_namespaces", "-1", "user_namespace_configuration", Unknown},
		{"/proc/sys/user/max_user_namespaces", strings.Repeat("9", 100), "user_namespace_configuration", Unknown},
		{"/sys/fs/cgroup/cgroup.controllers", "cpu memory", "cgroup_v2_controllers", Unavailable},
		{"/sys/fs/cgroup/cgroup.controllers", "xcpu memory pids", "cgroup_v2_controllers", Unavailable},
		{"/sys/kernel/security/lsm", "capability,landlock", "host_lsm_configuration", Unavailable},
		{"/sys/kernel/security/lsm", "notapparmor", "host_lsm_configuration", Unavailable},
	} {
		p := baselineProbe()
		p.files[test.path] = test.value
		r := InspectHost(p)
		if c := findCheck(t, r, test.control); c.Status != test.status {
			t.Fatal(c)
		}
	}
}
func TestPreflightUnsupportedHost(t *testing.T) {
	p := baselineProbe()
	p.goos = "darwin"
	r := InspectHost(p)
	if len(p.reads) != 0 || findCheck(t, r, "linux_host").Status != Unavailable || r.RequireReady() == nil {
		t.Fatal("unsupported platform permitted")
	}
	r = InspectHost(nil)
	if findCheck(t, r, "host_probe").Status != Unknown || r.RequireReady() == nil {
		t.Fatal("missing probe permitted")
	}
}
func TestPreflightDeterministicAndNoEnvironment(t *testing.T) {
	a := InspectHost(baselineProbe())
	t.Setenv("PATH", "/attacker/bin")
	t.Setenv("QI_WEB_TOKEN", "secret-never-output")
	b := InspectHost(baselineProbe())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("environment influenced probe")
	}
}
