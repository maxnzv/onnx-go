## ONNX Go library
This library provides functions to send files to ONNX servers:
- sherpa-onnx-online-websocket-server
- sherpa-onnx-offline-websocket-server
- banafo python APRI script
# Installation
To install the ONNX Go library, run:
'''bash
go get github.com/maxnzv/onnx-go
```
# Usage
Import the library:
'''go
import onnxgo "github.com/maxnzv/onnx-go"
'''
Use the library to send files to a server:
'''go
err := onnxgo.SendOfflineWS("server_url", "file_path", "")
if err != nil {
    log.Fatal(err)
}
'''