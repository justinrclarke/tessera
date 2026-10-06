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
