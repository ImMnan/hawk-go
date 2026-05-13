package pkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type gitLastCommitResponse struct {
	CommitId string `json:"commit_id"`
}

func getLastCommitId(sourceName string) (string, error) {

	apiServerEndpoint := os.Getenv("HAWK_API_SERVER_SVC")
	if apiServerEndpoint == "" {
		fmt.Println("No svc endpoint found, using hawk.k8s.net")
		apiServerEndpoint = "hawk.k8s.net:80"
	}
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

func postResultToHawkAPI(requestBodyData []byte) error {

	// This file will be placed in the shared volume - sharedVolume/source.name/commitId/output.json
	// Use output.json as a post request payload to the below hawk API request.

	// Post to http://hawk.k8s.net/api/document with bodyPayload
	//fmt.Printf("Now posting this payload %v\n", bodyPayload)

	apiServerEndpoint := os.Getenv("HAWK_API_SERVER_SVC")
	if apiServerEndpoint == "" {
		fmt.Println("No svc endpoint found, using hawk.k8s.net")
		apiServerEndpoint = "hawk.k8s.net:80"
	}

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
