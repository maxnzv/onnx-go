package onnxgo

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pingPeriod = 5 * time.Second
	pongWait   = 5 * time.Second
	writeWait  = 10 * time.Second
)

// Message structure for online model results
type Message struct {
	Segment int    `json:"segment"`
	Text    string `json:"text"`
	Final   bool   `json:"final"`
}

// WebSocketWriter wraps the WebSocket connection with a mutex for thread-safe writing.
type WebSocketWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// NewWebSocketWriter creates a new WebSocketWriter.
func NewWebSocketWriter(conn *websocket.Conn) *WebSocketWriter {
	return &WebSocketWriter{conn: conn}
}

// WriteMessage writes a message to the WebSocket connection in a thread-safe manner.
func (w *WebSocketWriter) WriteMessage(messageType int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return w.conn.WriteMessage(messageType, data)
}

// ReadMessage reads a message from the WebSocket connection in a thread-safe manner.
func (w *WebSocketWriter) ReadMessage() (int, []byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.conn.ReadMessage()
}

// Function to read WAV file and convert it to the required format
func readWave(filePath string) ([]float32, error) {
	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read the file header to get sample rate and other info
	header := make([]byte, 44)
	if _, err := io.ReadFull(file, header); err != nil {
		return nil, err
	}

	// Check if the file is single channel and 16-bit
	if binary.LittleEndian.Uint16(header[22:24]) != 1 {
		return nil, errors.New("unsupported number of channels, must be 1")
	}
	if binary.LittleEndian.Uint16(header[34:36]) != 16 {
		return nil, errors.New("unsupported bit depth, must be 16")
	}

	// Read audio data
	audioData, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	// Convert int16 samples to float32 and normalize to range [-1, 1]
	numSamples := len(audioData) / 2
	samples := make([]float32, numSamples)
	for i := 0; i < numSamples; i++ {
		sample := int16(binary.LittleEndian.Uint16(audioData[i*2 : (i+1)*2]))
		samples[i] = float32(sample) / 32768.0
	}

	return samples, nil
}

func concatMap(m map[int]string) string {
	var result string
	for i := 0; i < len(m); i++ {
		result += m[i] + "\n"
	}
	return result
}

// Receives results from online ws server
func receiveResults(conn *websocket.Conn, recvWg *sync.WaitGroup, result *string) {
	defer recvWg.Done()
	lastMessage := make(map[int]string)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("Message read error:", err)
			*result = ""
			return
		}

		if string(message) != "Done!" {
			var msg Message
			if err := json.Unmarshal(message, &msg); err != nil {
				log.Println("json unmarshal error:", err)
				*result = ""
				return
			}
			lastMessage[msg.Segment] = msg.Text
			// log.Printf("Message: %s", string(message))
			// log.Printf("Segment: %d, text: %s", msg.Segment, msg.Text)
			if msg.Final {
				// fmt.Println("\nThe final result is:\n", concatMap(lastMessage))
				*result = concatMap(lastMessage)
				return
			}
		} else {
			// log.Printf("Message: %s", string(message))
			// fmt.Println("\nFinal result is:\n", concatMap(lastMessage))
			*result = concatMap(lastMessage)
			return
		}
	}
}

// Sends a file via websocket to a online server
func SendOnlineWS(serverURI, filePath, sourceFile string, samplesPerMessage int, secondsPerMessage float64) (string, error) {
	audioData, err := readWave(filePath)
	log.Printf("Sending file %s online", sourceFile)
	if err != nil {
		log.Printf("failed to read wave file %s: %v", sourceFile, err)
		return "", err
	}

	conn, _, err := websocket.DefaultDialer.Dial(serverURI, nil)
	if err != nil {
		return "", logErrorf("failed to connect to server: %v", err)
	}
	defer conn.Close()

	wsWriter := NewWebSocketWriter(conn)

	var recvWg sync.WaitGroup
	recvWg.Add(1)
	var result string
	go receiveResults(conn, &recvWg, &result)

	conn.SetPingHandler(func(appData string) error {
		log.Println("Received ping, sending pong")
		if err := wsWriter.WriteMessage(websocket.PongMessage, []byte(appData)); err != nil {
			log.Println("write pong error:", err)
			return err
		}
		return nil
	})

	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := wsWriter.WriteMessage(websocket.PingMessage, nil); err != nil {
					log.Println("write ping error:", err)
					return
					// } else {
					//     log.Println("Ping sent")
				}
			}
		}
	}()

	for start := 0; start < len(audioData); start += samplesPerMessage {
		end := start + samplesPerMessage
		if end > len(audioData) {
			end = len(audioData)
		}
		// log.Printf("File: %s, start: %d, end: %d", sourceFile, start, end)

		buf := new(bytes.Buffer)
		for _, sample := range audioData[start:end] {
			binary.Write(buf, binary.LittleEndian, sample)
		}

		err := wsWriter.WriteMessage(websocket.BinaryMessage, buf.Bytes())
		if err != nil {
			log.Println("write:", err)
			return "", err
		}

		time.Sleep(time.Duration(secondsPerMessage * float64(time.Second)))
	}

	if err := wsWriter.WriteMessage(websocket.TextMessage, []byte("Done")); err != nil {
		log.Println("write close error:", err)
		return "", err
	}

	log.Printf("File %s sent", filePath)
	recvWg.Wait()

	return result, nil
}

// Sends a file via websocket to a offline server
func SendOfflineWS(serverURI, filePath string, sourceFile string) (string, error) {
	// Sends the file via websockets
	conn, _, err := websocket.DefaultDialer.Dial(serverURI, nil)
	if err != nil {
		return "", logErrorf("failed to connect to server: %v", err)
	}
	defer conn.Close()

	wsWriter := NewWebSocketWriter(conn)

	conn.SetPingHandler(func(appData string) error {
		log.Println("Received ping, sending pong")
		if err := wsWriter.WriteMessage(websocket.PongMessage, []byte(appData)); err != nil {
			log.Println("write pong error:", err)
			return err
		}
		return nil
	})

	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := wsWriter.WriteMessage(websocket.PingMessage, nil); err != nil {
					log.Println("write ping error:", err)
					return
					// } else {
					//     log.Println("Ping sent")
				}
			}
		}
	}()

	file, err := os.Open(filePath)
	if err != nil {
		return "", logErrorf("failed to open file: %v", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return "", logErrorf("failed to stat file: %v", err)
	}

	buffer := make([]byte, stat.Size())
	_, err = file.Read(buffer)
	if err != nil {
		return "", logErrorf("failed to read file: %v", err)
	}

	err = wsWriter.WriteMessage(websocket.BinaryMessage, buffer)
	if err != nil {
		return "", logErrorf("failed to send file: %v", err)
	}

	// log.Printf("Successfully sent file: %s", filePath)
	log.Printf("Successfully sent file: %s", sourceFile)
	// Receive decoding results
	var decodingResults []byte
	for {
		_, decodingResults, err = conn.ReadMessage()
		if string(decodingResults) == "Alive" {
			// log.Println("Alive received")
			continue
		}
		if err != nil || string(decodingResults) != "Alive" {
			break
		}
	}
	if err != nil {
		log.Println("failed to read decoding results: ", err)
		// log.Println ("Decoding results:", string (decodingResults))
		return "", err
	}
	// log.Println(string(decodingResults))

	// Signal that the client has sent all the data
	if err = wsWriter.WriteMessage(websocket.TextMessage, []byte("Done")); err != nil {
		log.Println("failed to send completion signal: ", err)
		return "", err
	}

	time.Sleep(100 * time.Millisecond)

	var resultJSON map[string]interface{}

	err = json.Unmarshal([]byte(decodingResults), &resultJSON)
	if err != nil {
		log.Println("Error decoding JSON:", err)
		return string(decodingResults), err
	}

	return resultJSON["text"].(string), nil
}

// Sends a file via script
func SendScript(script, language, filePath, sourceFile string) (string, error) {
	// Sends the file via script
	// cmd := exec.Command("python3", script, "api-wss", filePath, language)
	cmd := exec.Command(script, "api-wss", filePath, language)
	output, err := cmd.CombinedOutput()
	// log.Println(string(output))
	if err == nil {
		log.Printf("Sent file: %s, err: %v", sourceFile, err)
		return string(output), nil
	} else {
		log.Println("Failed to send file", sourceFile, "Error:", err)
		return "", err
	}
}

// Logs error
func logErrorf(format string, v ...interface{}) error {
	err := fmt.Errorf(format, v...)
	log.Print(err)
	return err
}
