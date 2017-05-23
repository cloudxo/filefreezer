// Copyright 2017, Timothy Bogdala <tdb@animal-machine.com>
// See the LICENSE file for more details.

package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/tbogdala/filefreezer"
	"github.com/tbogdala/filefreezer/cmd/freezer/models"
)

const (
	syncStatusMissing     = 1
	syncStatusLocalNewer  = 2
	syncStatusRemoteNewer = 3
	syncStatusSame        = 4
)

func runSyncFile(hostURI string, token string, localFilename string, remoteFilepath string) (status int, changeCount int, e error) {
	var getReq models.FileGetByNameRequest
	var remote models.FileGetResponse

	// get the file information for the filename, which provides
	// all of the information necessary to determine what to sync.
	getReq.FileName = remoteFilepath
	target := fmt.Sprintf("%s/api/file/name", hostURI)
	body, err := runAuthRequest(target, "GET", token, getReq)

	// if the file is not registered with the storage server, then upload it ...
	// futher checking will be unnecessary.
	if err != nil {
		localChunkCount, localLastMod, localHash, err := filefreezer.CalcFileHashInfo(*flagChunkSize, localFilename)
		if err != nil {
			return syncStatusMissing, 0, fmt.Errorf("Failed to calculate the file hash data for %s: %v", localFilename, err)
		}
		ulCount, err := syncUpload(hostURI, token, localFilename, remoteFilepath, localLastMod, localChunkCount, localHash)
		if err != nil {
			return syncStatusMissing, ulCount, fmt.Errorf("Failed to upload the file to the server %s: %v", hostURI, err)
		}
		return syncStatusLocalNewer, ulCount, nil
	}

	// we got a valid response so the file is registered on the server;
	// continue checking...
	err = json.Unmarshal(body, &remote)
	if err != nil {
		return 0, 0, fmt.Errorf("Failed to get the file information for the file name given (%s): %v", remoteFilepath, err)
	}

	// if the local file doesn't exist then download the file from the server if
	// it is registered there.
	if _, err := os.Stat(localFilename); os.IsNotExist(err) {
		dlCount, err := syncDownload(hostURI, token, remote.FileID, localFilename, remoteFilepath, remote.ChunkCount)
		return syncStatusRemoteNewer, dlCount, err
	}

	// calculate some of the local file information
	localChunkCount, localLastMod, localHash, err := filefreezer.CalcFileHashInfo(*flagChunkSize, localFilename)
	if err != nil {
		return 0, 0, fmt.Errorf("Failed to calculate the file hash data for %s: %v", localFilename, err)
	}

	// lets prove that we don't need to do anything for some cases
	// NOTE: a lastMod difference here doesn't trigger a difference if other metrics check out the same
	if localHash == remote.FileHash && len(remote.MissingChunks) == 0 && localChunkCount == remote.ChunkCount {
		different := false
		if *flagExtraStrict {
			// now we get a chunk list for the file
			var remoteChunks models.FileChunksGetResponse
			target := fmt.Sprintf("%s/api/chunk/%d", hostURI, remote.FileID)
			body, err := runAuthRequest(target, "GET", token, nil)
			err = json.Unmarshal(body, &remoteChunks)
			if err != nil {
				return 0, 0, fmt.Errorf("Failed to get the file chunk list for the file name given (%s): %v", remoteFilepath, err)
			}

			// sanity check
			remoteChunkCount := len(remoteChunks.Chunks)
			if localChunkCount == remoteChunkCount {
				// check the local chunks against remote hashes
				err = forEachChunk(int(*flagChunkSize), localFilename, localChunkCount, func(i int, b []byte) (bool, error) {
					// hash the chunk
					hasher := sha1.New()
					hasher.Write(b)
					hash := hasher.Sum(nil)
					chunkHash := base64.URLEncoding.EncodeToString(hash)

					// do the hashes match?
					if strings.Compare(chunkHash, remoteChunks.Chunks[i].ChunkHash) != 0 {
						// FIXME: At this point we have a chunk difference and it should be left to
						// the client as to which source to trust for the correct file, local or remote.
						different = true
						return false, nil
					}
					return true, nil
				})
				if err != nil {
					return 0, 0, fmt.Errorf("Failed to check the local file (%s) against the remote hashes: %v", localFilename, err)
				}
			}
		}

		// after whole-file hashs and all chunk hashs match, we can feel safe in saying they're not different
		if !different {
			log.Printf("%s --- unchanged", remoteFilepath)
			return syncStatusSame, 0, nil
		}
	}

	// at this point we have a file difference. we'll use the local file as the source of truth
	// if it's lastMod is newer than the remote file.
	if localLastMod > remote.LastMod {
		ulCount, e := syncUploadNewer(hostURI, token, remote.FileID, localFilename, remoteFilepath, localLastMod, localChunkCount, localHash)
		return syncStatusLocalNewer, ulCount, e
	}

	if localLastMod < remote.LastMod {
		dlCount, e := syncDownload(hostURI, token, remote.FileID, localFilename, remoteFilepath, remote.ChunkCount)
		return syncStatusRemoteNewer, dlCount, e
	}

	// there's been a difference detected in the files, but the mod times were the same, so
	// we attempt to upload any missing chunks.
	if len(remote.MissingChunks) > 0 {
		ulCount, e := syncUploadMissing(hostURI, token, remote.FileID, localFilename, remoteFilepath, localChunkCount)
		return syncStatusMissing, ulCount, e
	}

	// we checked to make sure it was the same above, but we found it different -- however, no steps to
	// resolve this were taken, so through an error.
	return 0, 0, fmt.Errorf("found differences between local (%s) and remote (%s) versions, but this was not reconcilled", localFilename, remoteFilepath)
}

func syncUploadMissing(hostURI string, token string, remoteID int, filename string, remoteFilepath string, localChunkCount int) (uploadCount int, e error) {
	// upload each chunk
	err := forEachChunk(int(*flagChunkSize), filename, localChunkCount, func(i int, b []byte) (bool, error) {
		// hash the chunk
		hasher := sha1.New()
		hasher.Write(b)
		hash := hasher.Sum(nil)
		chunkHash := base64.URLEncoding.EncodeToString(hash)

		target := fmt.Sprintf("%s/api/chunk/%d/%d/%s", hostURI, remoteID, i, chunkHash)
		body, err := runAuthRequest(target, "PUT", token, b)
		if err != nil {
			return false, err
		}

		var resp models.FileChunkPutResponse
		err = json.Unmarshal(body, &resp)
		if err != nil || resp.Status == false {
			return false, fmt.Errorf("Failed to upload the chunk to the server: %v", err)
		}

		log.Printf("%s +++ %d / %d", remoteFilepath, i+1, localChunkCount)
		uploadCount++

		return true, nil
	})
	if err != nil {
		return uploadCount, fmt.Errorf("Failed to upload the local file chunk for %s: %v", filename, err)
	}

	return uploadCount, nil
}

func syncUploadNewer(hostURI string, token string, remoteFileID int, filename string, remoteFilepath string,
	localLastMod int64, localChunkCount int, localHash string) (uploadCount int, e error) {
	// delete the remote file
	target := fmt.Sprintf("%s/api/file/%d", hostURI, remoteFileID)
	_, err := runAuthRequest(target, "DELETE", token, nil)
	if err != nil {
		return 0, fmt.Errorf("Failed to remove the file %d: %v", remoteFileID, err)
	}
	log.Printf("%s XXX deleted remote", filename)

	return syncUpload(hostURI, token, filename, remoteFilepath, localLastMod, localChunkCount, localHash)
}

func syncUpload(hostURI string, token string, filename string, remoteFilepath string, localLastMod int64, localChunkCount int, localHash string) (uploadCount int, e error) {
	// establish a new file on the remote freezer
	var putReq models.FilePutRequest
	putReq.FileName = remoteFilepath
	putReq.LastMod = localLastMod
	putReq.ChunkCount = localChunkCount
	putReq.FileHash = localHash
	target := fmt.Sprintf("%s/api/files", hostURI)
	body, err := runAuthRequest(target, "POST", token, putReq)
	if err != nil {
		return 0, err
	}

	var putResp models.FilePutResponse
	err = json.Unmarshal(body, &putResp)
	if err != nil {
		return 0, err
	}
	remoteID := putResp.FileID

	// upload each chunk
	err = forEachChunk(int(*flagChunkSize), filename, localChunkCount, func(i int, b []byte) (bool, error) {
		// hash the chunk
		hasher := sha1.New()
		hasher.Write(b)
		hash := hasher.Sum(nil)
		chunkHash := base64.URLEncoding.EncodeToString(hash)

		target = fmt.Sprintf("%s/api/chunk/%d/%d/%s", hostURI, remoteID, i, chunkHash)
		body, err = runAuthRequest(target, "PUT", token, b)
		if err != nil {
			return false, err
		}

		var resp models.FileChunkPutResponse
		err = json.Unmarshal(body, &resp)
		if err != nil || resp.Status == false {
			return false, fmt.Errorf("Failed to upload the chunk to the server: %v", err)
		}

		log.Printf("%s >>> %d / %d", remoteFilepath, i+1, localChunkCount)
		uploadCount++

		return true, nil
	})
	if err != nil {
		return uploadCount, fmt.Errorf("Failed to upload the local file chunk for %s: %v", filename, err)
	}

	log.Printf("%s ==> uploaded", remoteFilepath)
	return uploadCount, nil
}

func syncDownload(hostURI string, token string, remoteID int, filename string, remoteFilepath string, chunkCount int) (downloadCount int, e error) {
	localFile, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.ModePerm)
	if err != nil {
		return 0, fmt.Errorf("Failed to open local file (%s) for writing: %v", filename, err)
	}
	defer localFile.Close()

	// download each chunk and write it out to the file
	chunksWritten := 0
	for i := 0; i < chunkCount; i++ {
		target := fmt.Sprintf("%s/api/chunk/%d/%d", hostURI, remoteID, i)
		body, err := runAuthRequest(target, "GET", token, nil)
		if err != nil {
			return chunksWritten, fmt.Errorf("Failed to get the file chunk #%d for file id%d: %v", i, remoteID, err)
		}

		var chunkResp models.FileChunkGetResponse
		err = json.Unmarshal(body, &chunkResp)
		if err != nil {
			return chunksWritten, fmt.Errorf("Failed to get the file chunk #%d for file id%d: %v", i, remoteID, err)
		}

		// trim the buffer at the EOF marker of byte(0)
		chunk := chunkResp.Chunk.Chunk
		eofIndex := bytes.IndexByte(chunk, byte(0))
		if eofIndex > 0 && eofIndex < len(chunk) {
			chunk = chunk[:eofIndex]
		}

		_, err = localFile.Write(chunk)
		if err != nil {
			return chunksWritten, fmt.Errorf("Failed to write to the #%d chunk to the local file %s: %v", i, filename, err)
		}

		log.Printf("%s <<< %d / %d", remoteFilepath, i+1, chunkCount)
		chunksWritten++
	}

	log.Printf("%s <== downloaded", remoteFilepath)
	return chunksWritten, nil
}
