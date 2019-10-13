package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

const (
	serverURL = "http://localhost:8090/api/v1"
	authToken = "8W5zQ8nrSHe4NdBG"
	canvasId  = "e90af7cf-2164-4ce3-b831-3fbb7d1449ae"
)

/*
func readStreamingEndpoint(url string, output chan<- interface{}) {
	defer close(output)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("%v", err.Error())
		return
	}
	req.Header.Add("Private-Token", authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("%v", err.Error())
		return
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("%v response is %v\n", url, resp.StatusCode)
		return
	}

	var buff [1]byte
	var line []byte
Loop:
	for {
		n, err := resp.Body.Read(buff[:])
		if err != nil {
			//log.Printf("Got error %v on Read call", err.Error())
			break Loop
		}
		if n == 0 {
			//log.Printf("Get zero read on Read call")
			continue Loop
		}
		if buff[0] == '\n' && len(line) > 0 {
			var js interface{}
			err := json.Unmarshal(line, &js)
			if err != nil {
				log.Printf("Received invalid JSON from the endpoint. Error: %v\n", err.Error())
				break Loop
			}
			output <- js
			line = nil
		} else if buff[0] != '\n' {
			line = append(line, buff[0])
		}
	}

	log.Println("Read endpoint loop done")
}*/

type apiCallError struct {
	code int
	body []byte
}

func (e apiCallError) Error() string {
	return fmt.Sprintf("Response status code is %v, response body: %s",
		e.code, e.body)
}

func NewApiCallError(resp *http.Response) apiCallError {
	var e apiCallError
	e.code = resp.StatusCode
	e.body, _ = ioutil.ReadAll(resp.Body)
	return e
}

func readStreamingEndpoint(url string, output chan<- []byte) error {
	defer close(output)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Add("Private-Token", authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return NewApiCallError(resp)
	}

	var buff [1]byte
	var line []byte
Loop:
	for {
		n, err := resp.Body.Read(buff[:])
		if err != nil {
			return err
		}
		if n == 0 {
			continue Loop
		}
		if buff[0] == '\n' {
			if len(line) > 0 {
				output <- line
			}
			line = nil
		} else {
			line = append(line, buff[0])
		}
	}
	return nil
}

type WidgetSize struct {
	Height int `json:"height"`
	Width  int `json:"width"`
}

type CanvasImage struct {
	WidgetId   string     `json:"id"`
	WidgetType string     `json:"widget_type"`
	State      string     `json:"state"`
	Hash       string     `json:"hash"`
	Size       WidgetSize `json:"size"`
}

type ImageStatus struct {
	image     CanvasImage
	annotated bool
}

var gImageDB map[string]*ImageStatus

func updateImageStatus(imageStatus *ImageStatus) {
	image := imageStatus.image
	if len(image.Hash) == 0 {
		return
	}
	go annotateImage(image)
	imageStatus.annotated = true
}

func updateImageDB(image CanvasImage) {
	if image.WidgetType != "Image" {
		log.Printf("Unexpected widget type: %s", image.WidgetType)
		return
	}
	if len(image.WidgetId) == 0 {
		log.Printf("Empty widget id")
		return
	}
	var imageAlive bool
	switch image.State {
	case "normal":
		imageAlive = true
	case "deleted":
		imageAlive = false
	default:
		log.Printf("Unexpected widget state: %s", image.State)
		return
	}

	imageStatus, ok := gImageDB[image.WidgetId]
	if !ok {
		imageStatus := &ImageStatus{
			image:     image,
			annotated: false,
		}
		gImageDB[image.WidgetId] = imageStatus
		updateImageStatus(imageStatus)
	} else {
		if imageAlive {
			if !imageStatus.annotated {
				imageStatus.image = image
				updateImageStatus(imageStatus)
			}
		} else {
			delete(gImageDB, image.WidgetId)
		}
	}
}

func processRawJsonStream(input <-chan []byte) {
	for {
		rawJson, ok := <-input
		if !ok {
			return
		}
		imageList := make([]CanvasImage, 0, 1)
		log.Printf("Raw json: %s", rawJson)
		err := json.Unmarshal(rawJson, &imageList)
		if err != nil {
			log.Print(err.Error())
		} else {
			for _, image := range imageList {
				updateImageDB(image)
			}
		}
	}
}

func doMainLoop() {
	url := serverURL + "/canvases/" + canvasId + "/images?subscribe"
	for {
		rawJsonStream := make(chan []byte)
		go processRawJsonStream(rawJsonStream)
		err := readStreamingEndpoint(url, rawJsonStream)
		if apiErr, ok := err.(apiCallError); ok {
			log.Print(apiErr.Error())
			return // User needs to change config
		}
		// The network error, sleep and try again
		const timeout = 3 * time.Second
		log.Printf("Error %v, reconnect after %s", err.Error(), timeout)
		time.Sleep(timeout)
	}
}

func downloadImage(imageId string, originalName string) {
	const (
		downloadDir = "/home/igor/Desktop/api_downloads"
	)

	if stat, err := os.Stat(downloadDir); err != nil || !stat.IsDir() {
		log.Printf("Downloads folder %v does not exists or cannot be accessed %v\n", downloadDir, err)
		return
	}

	ext := path.Ext(originalName)
	if len(ext) == 0 {
		ext = ".jpg" // Make a guess
	}

	fullPath := path.Join(downloadDir, imageId+ext)
	if _, err := os.Stat(fullPath); !os.IsNotExist(err) {
		log.Printf("Skip existing file %v\n", fullPath)
		return
	} else {
		log.Printf("Going to download %v\n", fullPath)
	}

	endpoint := serverURL + "/canvases/" + canvasId + "/images/" + imageId + "/download"
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		log.Printf("%v\n", err.Error())
		return
	}
	req.Header.Add("Private-Token", authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("%v\n", err.Error())
		return
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("%v response is %v\n", endpoint, resp.StatusCode)
		return
	}

	outFile, err := os.Create(fullPath)
	if err != nil {
		log.Printf("Cannot create output file, error: %v\n", err)
		return
	}

	outWriter := bufio.NewWriter(outFile)

	written, err := io.Copy(outWriter, resp.Body)
	if err != nil {
		log.Printf("Error while copying download data to output: %v\n", err)
		return
	} else {
		log.Printf("Written %v bytes\n", written)
	}
}

func annotateImage(image CanvasImage) {
	url := serverURL + "/canvases/" + canvasId + "/notes"
	_ = url

	pos_x := image.Size.Width - 150
	if pos_x < 0 {
		pos_x = image.Size.Width
	}
	pos_y := image.Size.Height - 400
	if pos_y < 0 {
		pos_y = image.Size.Height
	}

	note := fmt.Sprintf(`{
		"parent_id" : "%s",
		"text": "Hello from API",
		"depth": 1,
		"background_color" : "#99ff33",
		"location": {
			"x": %v,
			"y": %v
		}
	}`, image.WidgetId, pos_x, pos_y)

	log.Printf("Gonna annotate with JSON: %s", note)

	req, err := http.NewRequest("POST", url, strings.NewReader(note))
	if err != nil {
		log.Printf(err.Error())
		return
	}
	req.Header.Add("Private-Token", authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf(err.Error())
		return
	}

	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("Status code is %v, response: %s", resp.StatusCode, body)
		return
	}

	log.Printf("Annotated %s", image.WidgetId)
}

func main() {
	gImageDB = make(map[string]*ImageStatus)
	doMainLoop()
}
