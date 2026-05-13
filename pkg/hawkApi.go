package pkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

type gitLastCommitResponse struct {
	CommitId string `json:"commit_id"`
}

func resolveAPIServerEndpoint(syncCfg SyncConfig) string {
	apiServerEndpoint := strings.TrimSpace(syncCfg.APIServers.Connection.SvcName)
	if apiServerEndpoint != "" {
		return apiServerEndpoint
	}

	apiServerEndpoint = strings.TrimSpace(os.Getenv("HAWK_API_SERVER_SVC"))
	if apiServerEndpoint == "" {
		fmt.Println("No api server serviceName found in config, using hawk.k8s.net:80")
		apiServerEndpoint = "hawk.k8s.net:80"
	}

	return apiServerEndpoint
}

func getLastCommitId(sourceName, apiServerEndpoint string) (string, error) {

	client := &http.Client{}
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/api/document/getlastcommitid/%v", apiServerEndpoint, sourceName), nil)
	fmt.Printf("hitting API endpoint: %v\n", req)

	if err != nil {
		log.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	bodyText, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", bodyText)
	var gitResponse gitLastCommitResponse
	json.Unmarshal(bodyText, &gitResponse)

	commitId := gitResponse.CommitId
	fmt.Printf("Last commit id for %v is %v\n", sourceName, commitId)
	return commitId, nil
}

func postResultToHawkAPI(requestBodyData []byte, apiServerEndpoint string) error {

	// This file will be placed in the shared volume - sharedVolume/source.name/commitId/output.json
	// Use output.json as a post request payload to the below hawk API request.

	// Post to http://hawk.k8s.net/api/document with bodyPayload
	//fmt.Printf("Now posting this payload %v\n", bodyPayload)

	client := &http.Client{}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/api/document/create", apiServerEndpoint), bytes.NewBuffer(requestBodyData))
	fmt.Printf("[DEBUG] hitting API endpoint: %v\n", req)

	if err != nil {
		log.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	bodyText, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", bodyText)

	return nil
}
