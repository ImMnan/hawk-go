package pkg

import (
	"fmt"
)

func sync(c Config) error {

	fmt.Printf("syncing %v\n", c.Name)

	return nil
}
