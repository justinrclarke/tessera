package host

import "tessera/internal/api"

type Info struct {
	CPU       int
	MemTotal  int64
	MemFree   int64
	DiskTotal int64
	DiskFree  int64
}

func Resources(path string) (api.Resources, api.Resources, int64, int64) {
	info := probe(path)
	cap := api.Resources{CPU: int64(info.CPU) * 1000, Memory: info.MemTotal}
	free := api.Resources{CPU: cap.CPU, Memory: info.MemFree}
	if free.Memory == 0 {
		free.Memory = info.MemTotal
	}
	return cap, free, info.DiskTotal, info.DiskFree
}
