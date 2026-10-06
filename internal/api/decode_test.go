package api

import "testing"

func TestDecodeGPUConstraints(t *testing.T) {
	obj, err := DecodeOne([]byte("kind: Model\nname: llm\nimage: model:1\ngpus: 1\ngpu_model: NVIDIA H100\ngpu_memory: 20Gi\n"))
	if err != nil {
		t.Fatal(err)
	}
	if obj.App == nil || obj.App.GPUModel != "NVIDIA H100" || obj.App.GPUMemory != 20<<30 || obj.App.SensitiveTo != "gpu" {
		t.Fatalf("model %+v", obj.App)
	}
	if _, err := DecodeOne([]byte("kind: App\nname: bad\nimage: model:1\ngpu_memory: 20Gi\n")); err == nil {
		t.Fatal("accepted GPU memory without a GPU request")
	}
	if _, err := DecodeOne([]byte("kind: App\nname: bad\nimage: model:1\ngpus: -1\n")); err == nil {
		t.Fatal("accepted a negative GPU request")
	}
}

func TestGangFabricRequiresGangJob(t *testing.T) {
	good, err := DecodeOne([]byte("kind: Job\nname: train\nimage: trainer:1\ngang: true\ngang_fabric: fabric\nnode_labels:\n  storage: shared\n"))
	if err != nil || good.App == nil || good.App.GangFabric != "fabric" || good.App.NodeLabels["storage"] != "shared" {
		t.Fatalf("gang job %+v: %v", good.App, err)
	}
	if _, err := DecodeOne([]byte("kind: App\nname: bad\nimage: trainer:1\ngang_fabric: fabric\n")); err == nil {
		t.Fatal("accepted fabric requirement on an App")
	}
}

func TestManifestRejectsIgnoredSettingsAndAllowsScaleToZero(t *testing.T) {
	base := "kind: App\nname: web\nimage: nginx\n"
	for _, field := range []string{"repilcas: 2\n", "health: {path: /ready, port: 80}\n", "volumes: []\n", "replicas: -1\n", "resources: {memroy: 128Mi}\n", "resources: {cpu: -100m}\n", "ports: [{container: 80, protocol: UDP}]\n"} {
		if _, err := DecodeOne([]byte(base + field)); err == nil {
			t.Fatalf("silently accepted invalid or unimplemented setting: %s", field)
		}
	}
	for _, tc := range []struct {
		field    string
		replicas int
	}{{"", 1}, {"replicas: 0\n", 0}, {"replicas: 2\n", 2}} {
		obj, err := DecodeOne([]byte(base + tc.field))
		if err != nil || obj.App.Replicas != tc.replicas {
			t.Fatalf("replica setting %q: %+v %v", tc.field, obj.App, err)
		}
	}
	if _, err := DecodeOne([]byte("kind: Policy\nmove: {min_gain: 0.2, cooldown: 5m}\n")); err != nil {
		t.Fatalf("valid nested move policy rejected: %v", err)
	}
}
