package function

import (
	"encoding/json"
	"os/exec"
)

type PM2Process struct {
	PID    int            `json:"pid"`
	Name   string         `json:"name"`
	PM2Env map[string]any `json:"pm2_env"`
	PM2ID  int            `json:"pm_id"`
	Monit  Pm2Monit       `json:"monit"`
}

type Pm2Monit struct {
	Memory int `json:"memory"`
	CPU    int `json:"cpu"`
}

func GetPm2Process() ([]PM2Process, error) {
	str, err := exec.Command("pm2", "prettylist").Output()
	if err != nil {
		return []PM2Process{}, err
	}
	// Here you would typically unmarshal the JSON output from str into []PM2Process
	var processes []PM2Process
	err = json.Unmarshal(str, &processes)
	return processes, err
}
