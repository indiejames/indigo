package rpcclient

import "testing"

func TestDescribe(t *testing.T) {
	for _, tc := range []struct {
		cfg  DebugConfig
		want string
	}{
		{DebugConfig{Name: "server", Program: "/w/cmd/server"}, "server"},
		{DebugConfig{Program: "/w/cmd/server"}, "server"},
		{DebugConfig{Mode: "test", Program: "/w/pkg"}, "tests in pkg"},
		{DebugConfig{Mode: "test", Program: "/w/pkg", Args: []string{"-test.run", "^TestX$"}}, "TestX in pkg"},
		{DebugConfig{Mode: "test", Program: "/w/pkg", Args: []string{"-test.run", "^$", "-test.bench", "^BenchmarkY$"}}, "BenchmarkY in pkg"},
	} {
		if got := tc.cfg.Describe(); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.cfg, got, tc.want)
		}
	}
}
