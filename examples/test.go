package main

import (
	"container/list"
	"flag"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	onnxgo "github.com/maxnzv/onnx-go"
)

var (
	wavDirPath        string
	serverURI         string
	maxConcurrent     int
	script            string
	language          string
	onlineModel       bool
	samplesPerMessage int
	secondsPerMessage float64
)

var resultMu sync.Mutex

const resultFile = "results.txt"

func init() {
	flag.StringVar(&wavDirPath, "wavdir", "", "Directory to scan for sound files")
	flag.StringVar(&serverURI, "server", "", "WebSocket server URI")
	flag.StringVar(&script, "script", "", "Script to run instead of websocket")
	flag.StringVar(&language, "language", "", "Language to run the script to run instead of websocket")
	flag.IntVar(&maxConcurrent, "concurrent", 1, "Maximum number of concurrent file transfers")
	flag.BoolVar(&onlineModel, "onlinemodel", false, "Set to true is you want to use online model")
	flag.IntVar(&samplesPerMessage, "samples-per-message", 8000, "Number of samples per message")
	flag.Float64Var(&secondsPerMessage, "seconds-per-message", 0.1, "We will simulate that the duration of two messages is of this value")
	flag.Parse()
}

func main() {
	if wavDirPath == "" {
		log.Fatal("Directory must be specified")
	}

	if (serverURI == "" && script == "") || (serverURI != "" && script != "") {
		log.Fatal("Either serverURI on script must be specified")
	}

	if script != "" && language == "" {
		log.Fatal("There should be language specified if the script is")
	}

	wavFiles, err := scanDirectory(wavDirPath)
	if err != nil {
		log.Fatalf("Failed to scan directory: %v", err)
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	file_no := 0
	failed_no := list.New()

	for _, file := range wavFiles {
		file_no += 1
		var checkFilePath string
		if info, err := os.Stat(file); err == nil && !info.IsDir() {
			wg.Add(1)
			sem <- struct{}{}
			log.Println(file_no, "Sending file:", file)
			log.Println(file_no, "Check file:", checkFilePath)
			go func(f string) {
				defer wg.Done()
				defer func() { <-sem }()
				result, err := sendFile(f)
				if err != nil {
					failed_no.PushBack(f)
					writeResult(resultFile, f)
					writeResult(resultFile, ": FAILED\n")
					log.Printf("%v: Failed to send file %s: %v", failed_no.Len(), f, err)
					return
				} else {
					log.Printf("%v: Reply received for file %s", file_no, f)
					writeResult(resultFile, f)
					writeResult(resultFile, ": SUCCESS\n")
				}
				writeResult(resultFile, result+"\n")
			}(file)
		}
	}
	wg.Wait()
	log.Println("Files sent: ", file_no)
	log.Println("Failed files: ", failed_no.Len())
	for e := failed_no.Front(); e != nil; e = e.Next() {
		log.Println(e.Value)
	}
}

// Sends a file via websocket to a online server
func sendOnlineWS(filePath, sourceFile string) (string, error) {
	return onnxgo.SendOnlineWS(serverURI, filePath, sourceFile, samplesPerMessage, secondsPerMessage)
}

// Sends a file via websocket to a offline server
func sendOfflineWS(filePath string, sourceFile string) (string, error) {
	// Sends the file via websockets
	return onnxgo.SendOfflineWS(serverURI, filePath, sourceFile)
}

// Sends a file via script
func sendScript(filePath string, sourceFile string) (string, error) {
	// Sends the file via script
	// cmd := exec.Command("python3", script, "api-wss", filePath, language)
	return onnxgo.SendScript(script, language, filePath, sourceFile)
}

// Prepares a file for sending and calls corresponding send* func
func sendFile(sourceFile string) (string, error) {
	// Generate a name for the temporary file
	// destinationFile, err := os.CreateTemp("", "*.opus")
	destinationFile, err := os.CreateTemp("", "*.wav")
	if err != nil {
		log.Println("Failed to create source file:", err)
		return "", err
	}
	defer os.Remove(destinationFile.Name())

	filePath := destinationFile.Name()

	// ffmpegCommand := exec.Command("ffmpeg", "-i", sourceFile, "-v", "quiet", "-acodec", "libopus", "-ar", "16000", "-ac", "1", "-b:a", "64k", "-y", "-vn", filePath)
	ffmpegCommand := exec.Command("ffmpeg", "-i", sourceFile, "-v", "quiet", "-acodec", "pcm_s16le", "-ac", "1", "-ar", "16000", "-sample_fmt", "s16", "-y", "-vn", filePath)
	if err := ffmpegCommand.Run(); err != nil {
		log.Println("FFmpeg failed to convert the file:", err)
		return "", err
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Println("Converted file does not exist:", err)
		return "", err
	}

	if serverURI != "" {
		if onlineModel {
			return sendOnlineWS(filePath, sourceFile)
		} else {
			return sendOfflineWS(filePath, sourceFile)
		}
	} else {
		if script != "" {
			return sendScript(filePath, sourceFile)
		}
	}
	return "", nil
}

// Scans a dir with sub-dirs and returns a file list.
func scanDirectory(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// Writes a string to a text file
func writeResult(filename, data string) {
	resultMu.Lock()
	defer resultMu.Unlock()

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Println("Error creating file: ", err)
		return
	}
	defer file.Close()

	_, err = file.WriteString(data)
	if err != nil {
		log.Println("Error writing to file: ", err)
		return
	}
}
