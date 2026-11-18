package function

type State struct {
	ConfigPath     string
	Config         Config
	Pm2ProcessList []PM2Process
}

func NewState() State {
	configPath := GetConfigPath()
	config, err := ReadConfig()
	if err != nil {
		panic(err)
	}
	pm2process, err := GetPm2Process()
	if err != nil {
		panic(err)
	}
	state := State{
		ConfigPath:     configPath,
		Config:         config,
		Pm2ProcessList: pm2process,
	}
	bindPm2Processes(&state)
	return state
}

func (state *State) Reload() error {
	config, err := ReadConfig()
	if err != nil {
		return err
	}
	pm2process, err := GetPm2Process()
	if err != nil {
		return err
	}
	state.Config = config
	state.Pm2ProcessList = pm2process
	bindPm2Processes(state)
	state.ConfigPath = GetConfigPath()
	return nil
}

func bindPm2Processes(state *State) {
	for i := range state.Config.Projects {
		state.Config.Projects[i].PM2Process = DetectPm2Binding(state, state.Config.Projects[i])
	}
}
