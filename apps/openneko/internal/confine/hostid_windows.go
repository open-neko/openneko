package confine

import "golang.org/x/sys/windows/registry"

func machineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	if v, _, err := k.GetStringValue("MachineGuid"); err == nil && v != "" {
		return "machine-guid:" + v
	}
	return ""
}
