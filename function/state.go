package function

type State struct {
	ConfigPath string
	Config     Config
}

func NewState() State {
	configPath := GetConfigPath()
	config, err := ReadConfig()
	if err != nil {
		panic(err)
	}
	return State{
		ConfigPath: configPath,
		Config:     config,
	}
}

func (state *State) Reload() error {
	config, err := ReadConfig()
	if err != nil {
		return err
	}
	state.Config = config
	state.ConfigPath = GetConfigPath()
	return nil
}
