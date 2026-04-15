package pkg

import (
	"fmt"
)

type ConfList []Config

type Config struct {
	Name        string
	Type        string
	ID          string
	Description string
	Sync        SyncConfig
}

type SyncConfig struct {
	Enabled     bool
	Mode        string
	AgentType   string
	Schedule    string
	Source      string
	Credentials credentialsConfig
	Forward     forwardConfig
}

type credentialsConfig struct {
	Type string
	Name string
}

type forwardConfig struct {
	Path string
}

func Run() {

	fmt.Println("Running the hawk...")
	// This is supposed to run 2 functions, one for the init and other for the main loop.

	fmt.Println("Initializing hawk configuraions...")

	confList, err := init_hawk()
	if err != nil {
		fmt.Printf("error encountered: %v\n", err)
		panic(err)
	}

	// For loop that is going to run the main loop for config list.
	// Iterate over conflist struct and proceed in the for loop.
	for _, c := range confList {
		fmt.Printf("[DEBUG] Processing config: %s\n", c.Name)
		// Here we would have the logic to process each config, for now we just print it.

		err := sync(c)
		if err != nil {
			panic(err)

		}
	}
}

func init_hawk() (ConfList, error) {

	// This function is main initialisation function, tieng all other sub-init functions.

	fmt.Println("Everythung work well, returning success")

	return nil, nil

}
