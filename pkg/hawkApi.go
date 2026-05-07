package pkg

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type doneJson struct {
	Title       string `json:"title"`
	SourceName  string `json:"source_name"`
	CommitId    string `json:"commit_id,omitempty"`
	Link        string `json:"link,omitempty"`
	WhatChanged string `json:"what_changed,omitempty"`
	ProductName string `json:"product_name,omitempty"`
	Tag         string `json:"tag,omitempty"`
}

func getLastCommitId(sourceName string) (string, error) {

	apiServerEndpoint := os.Getenv("HAWK_API_SERVER_SVC")
	if apiServerEndpoint == "" {
		fmt.Println("No svc endpoint found, using hawk.k8s.net")
		apiServerEndpoint = "hawk.k8s.net:80"
	}
	client := &http.Client{}
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/api/document/getlastcommitid/%v", apiServerEndpoint, sourceName), nil)
	fmt.Printf("%v\n", req)

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

	commitId := "66684777c6fc74c8fc85c13dfa3143fb551b56e3"
	return commitId, nil
}

func postResultToHawkAPI(bodyPayload doneJson) error {

	// Post to http://hawk.k8s.net/api/document with bodyPayload
	fmt.Printf("Now posting this payload %v\n", bodyPayload)
	return nil
}
