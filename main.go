package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	vision "cloud.google.com/go/vision/apiv1"
)

const (
	serverURL      = "http://localhost:8090/api/v1"
	authToken      = "8W5zQ8nrSHe4NdBG"
	canvasId       = "e90af7cf-2164-4ce3-b831-3fbb7d1449ae"
	gvisionKeyFile = "/home/igor/SRC2/gvision_keys.json"
)

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
	Height float64 `json:"height"`
	Width  float64 `json:"width"`
}

type CanvasImage struct {
	WidgetId   string     `json:"id"`
	WidgetType string     `json:"widget_type"`
	State      string     `json:"state"`
	Hash       string     `json:"hash"`
	Size       WidgetSize `json:"size"`
}

type CanvasNote struct {
	WidgetId        string `json:"id"`
	ParentId        string `json:"parent_id"`
	WidgetType      string `json:"widget_type"`
	State           string `json:"state"`
	Text            string `json:"text"`
	BackgroundColor string `json:"background_color"`
}

type ImageStatus struct {
	image     CanvasImage
	annotated bool
}

type ImageAnnotation struct {
	image *CanvasImage
	text  string
}

var gImageDB map[string]*ImageStatus
var gVisonQueue chan *CanvasImage
var gAnnotationQueue chan *ImageAnnotation

func updateImageStatus(imageStatus *ImageStatus) {
	image := imageStatus.image
	if len(image.Hash) == 0 {
		return
	}
	if imageStatus.annotated {
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
			imageStatus.image = image
			updateImageStatus(imageStatus)
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
		//log.Printf("Raw json: %s", rawJson)
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

func getImageDataStream(imageId string) (io.ReadCloser, error) {
	endpoint := serverURL + "/canvases/" + canvasId + "/images/" + imageId + "/download"
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Private-Token", authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%v response is %v\n", endpoint, resp.StatusCode)
	}

	return resp.Body, nil
}

var currentColor int
var magicColors []string = []string{
	"#ccff66",
	"#99ff66",
	"#66ff99",
	"#99ccff",
}

func getMagicColor() string {
	currentColor++
	return magicColors[currentColor%len(magicColors)]
}

func isMagicColor(color string) bool {
	for _, magic := range magicColors {
		if strings.EqualFold(color, magic) {
			return true
		}
	}
	return false
}

func checkAnnnotations(image CanvasImage) (bool, error) {
	url := serverURL + "/canvases/" + canvasId + "/notes"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Add("Private-Token", authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Status code is %v, response: %s", resp.StatusCode, body)
	}

	var notes []CanvasNote
	err = json.Unmarshal(body, &notes)
	if err != nil {
		return false, err
	}

	for _, note := range notes {
		if note.ParentId == image.WidgetId && isMagicColor(note.BackgroundColor) {
			return true, nil
		}
	}

	return false, nil
}

func annotateImage(image CanvasImage) {
	log.Printf("Check image %s", image.WidgetId)
	annotated, err := checkAnnnotations(image)
	if err != nil {
		log.Printf("Can't get info about existing annotations: %s", err.Error())
		return
	}
	if annotated {
		log.Printf("Image is already annotated")
		return
	}

	gVisonQueue <- &image
}

func attachNote(image *CanvasImage, noteColor string, noteText string) {
	url := serverURL + "/canvases/" + canvasId + "/notes"

	note_side := 300.0
	note_scale := (image.Size.Height / 4.5) / note_side

	pos_x := image.Size.Width - (note_side/2)*note_scale
	if pos_x < 0 {
		pos_x = image.Size.Width
	}
	pos_y := image.Size.Height - (note_side*1.33)*note_scale
	if pos_y < 0 {
		pos_y = image.Size.Height
	}

	note := fmt.Sprintf(`{
		"parent_id" : "%s",
		"text": "%s",
		"depth": 1,
		"background_color" : "%s",
		"size": {
			"width": %v,
			"height": %v
		},
		"location": {
			"x": %v,
			"y": %v
		},
		"scale" : %v
	}`, image.WidgetId,
		noteText,
		noteColor,
		note_side,
		note_side,
		pos_x,
		pos_y, note_scale)

	//log.Printf("Gonna annotate with JSON: %s", note)

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

func annotationLoop() {
Loop:
	for {
		annotation, ok := <-gAnnotationQueue
		if !ok {
			break Loop
		}

		attachNote(annotation.image, getMagicColor(), annotation.text)
	}
}

func gvisionLoop() {
	ctx := context.Background()

	// Creates a client.
	client, err := vision.NewImageAnnotatorClient(ctx)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()
Loop:
	for {
		canvasImage, ok := <-gVisonQueue
		if !ok {
			break Loop
		}
		data, err := getImageDataStream(canvasImage.WidgetId)
		if err != nil {
			gAnnotationQueue <- &ImageAnnotation{
				image: canvasImage,
				text:  fmt.Sprintf("Can't get data stream of image %s, error: %s", canvasImage.WidgetId, err.Error()),
			}
			continue Loop
		}
		defer data.Close()
		image, err := vision.NewImageFromReader(data)
		if err != nil {
			gAnnotationQueue <- &ImageAnnotation{
				image: canvasImage,
				text:  fmt.Sprintf("Can't get data stream of image %s, error: %s", canvasImage.WidgetId, err.Error()),
			}
			continue Loop
		}

		labels, err := client.DetectLabels(ctx, image, nil, 10)
		if err != nil {
			gAnnotationQueue <- &ImageAnnotation{
				image: canvasImage,
				text:  fmt.Sprintf("Can't get data stream of image %s, error: %s", canvasImage.WidgetId, err.Error()),
			}
			continue Loop
		}

		var descriptions []string
		for _, label := range labels {
			descriptions = append(descriptions, label.Description)
		}

		gAnnotationQueue <- &ImageAnnotation{
			image: canvasImage,
			text:  strings.Join(descriptions, "\r\n"),
		}
	}
}

func mainLoop() {
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

func main() {
	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", gvisionKeyFile)

	gImageDB = make(map[string]*ImageStatus)
	gVisonQueue = make(chan *CanvasImage)
	gAnnotationQueue = make(chan *ImageAnnotation)

	go gvisionLoop()
	go annotationLoop()

	mainLoop()
}
