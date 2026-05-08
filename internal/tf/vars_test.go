package tf

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jgruberf5/bnkctl/internal/config"
)

func TestRenderTFVars_CreateMode(t *testing.T) {
	ws := &config.Workspace{
		IBMCloud: config.IBMCloudCfg{Region: "us-south", ResourceGroup: "default"},
		Cluster: config.ClusterCfg{
			Create:           true,
			Name:             "bnk-demo",
			OpenShiftVersion: "4.18",
			WorkersPerZone:   2,
		},
	}
	var buf bytes.Buffer
	if err := RenderTFVars(&buf, ws, "", ""); err != nil {
		t.Fatalf("RenderTFVars: %v", err)
	}

	want := []string{
		`ibmcloud_cluster_region = "us-south"`,
		`ibmcloud_resource_group = "default"`,
		`create_roks_cluster = true`,
		`openshift_cluster_name = "bnk-demo"`,
		`openshift_cluster_version = "4.18"`,
		`roks_workers_per_zone = 2`,
	}
	out := buf.String()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing line: %s\noutput:\n%s", w, out)
		}
	}

	// Critical safety check: no api_key field (env-var path is mandatory).
	if strings.Contains(out, "api_key") {
		t.Errorf("api_key leaked into tfvars; must be passed via env var only.\noutput:\n%s", out)
	}
}

func TestRenderTFVars_AttachMode(t *testing.T) {
	ws := &config.Workspace{
		IBMCloud: config.IBMCloudCfg{Region: "us-south"},
		Cluster:  config.ClusterCfg{Create: false, Name: "existing-cluster"},
	}
	var buf bytes.Buffer
	if err := RenderTFVars(&buf, ws, "", ""); err != nil {
		t.Fatalf("RenderTFVars: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `roks_cluster_id_or_name = "existing-cluster"`) {
		t.Errorf("attach mode missing roks_cluster_id_or_name\noutput:\n%s", out)
	}
	if strings.Contains(out, "openshift_cluster_name") {
		t.Errorf("attach mode should not emit openshift_cluster_name\noutput:\n%s", out)
	}
	if !strings.Contains(out, "create_roks_cluster = false") {
		t.Errorf("missing create_roks_cluster = false\noutput:\n%s", out)
	}
}

func TestRenderTFVars_OmitsEmptyFields(t *testing.T) {
	ws := &config.Workspace{
		Cluster: config.ClusterCfg{Create: true, Name: "demo"},
	}
	var buf bytes.Buffer
	if err := RenderTFVars(&buf, ws, "", ""); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Region/RG were unset — should not appear.
	if strings.Contains(out, "ibmcloud_cluster_region") {
		t.Errorf("region should be omitted when empty\noutput:\n%s", out)
	}
}

func TestRenderTFVars_KubeconfigDir(t *testing.T) {
	ws := &config.Workspace{
		Cluster: config.ClusterCfg{Create: true, Name: "demo"},
	}
	var buf bytes.Buffer
	if err := RenderTFVars(&buf, ws, "/home/user/.bnkctl/default/state/kubeconfig", "/home/user/.bnkctl/default/state/scratch"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`kubeconfig_dir = "/home/user/.bnkctl/default/state/kubeconfig"`,
		`scratch_dir = "/home/user/.bnkctl/default/state/scratch"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s\noutput:\n%s", want, out)
		}
	}

	// Empty strings should NOT emit the lines — keeps tfvars clean for
	// callers that don't want this rendering.
	buf.Reset()
	if err := RenderTFVars(&buf, ws, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"kubeconfig_dir", "scratch_dir"} {
		if strings.Contains(buf.String(), k) {
			t.Errorf("empty %s should not emit a line\noutput:\n%s", k, buf.String())
		}
	}
}

func TestRenderTFVars_BNKFields(t *testing.T) {
	ws := &config.Workspace{
		Cluster: config.ClusterCfg{Create: true, Name: "demo"},
		BNK: config.BNKCfg{
			CNEInstanceSize: "Medium",
			FARRepoURL:      "repo.f5.com",
			ManifestVersion: "2.3.0-foo",
		},
	}
	var buf bytes.Buffer
	if err := RenderTFVars(&buf, ws, "", ""); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	want := []string{
		`cneinstance_deployment_size = "Medium"`,
		`far_repo_url = "repo.f5.com"`,
		`f5_bigip_k8s_manifest_version = "2.3.0-foo"`,
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing: %s", w)
		}
	}
}
