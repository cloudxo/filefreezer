// Copyright 2017, Timothy Bogdala <tdb@animal-machine.com>
// See the LICENSE file for more details.

package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"

	"strconv"

	"github.com/gorilla/mux"
	"github.com/tbogdala/filefreezer"
	"github.com/tbogdala/filefreezer/cmd/freezer/models"
)

// InitRoutes creates the routing multiplexer for the server
func InitRoutes(state *serverState) *mux.Router {
	// setup the web server routing table
	r := mux.NewRouter().StrictSlash(false)

	// setup the user login handler
	r.Handle("/api/users/login", handleUsersLogin(state)).Methods("POST")

	// returns the authenticated users's current stats such as quota, allocation and revision counts
	r.Handle("/api/user/stats", authenticateToken(state, handleGetUserStats(state))).Methods("GET")

	// updates the user's crypto hash used to verify the user-entered password client-side.
	r.Handle("/api/user/cryptohash", authenticateToken(state, handlePutUserCryptoHash(state))).Methods("PUT")

	// returns all files and their whole-file hash
	r.Handle("/api/files", authenticateToken(state, handleGetAllFiles(state))).Methods("GET")

	// handles registering a file to a user
	r.Handle("/api/files", authenticateToken(state, handlePutFile(state))).Methods("POST")

	// handles registering a new file version for a given file id
	r.Handle("/api/file/{fileid:[0-9]+}/version", authenticateToken(state, handleNewFileVersion(state))).Methods("POST")

	// returns a file information response with missing chunk list
	r.Handle("/api/file/{fileid:[0-9]+}", authenticateToken(state, handleGetFile(state))).Methods("GET")

	// handles registering a new file version for a given file id
	r.Handle("/api/file/{fileid:[0-9]+}/versions", authenticateToken(state, handleGetAllFileVersion(state))).Methods("Get")

	// deletes a file
	r.Handle("/api/file/{fileid:[0-9]+}", authenticateToken(state, handleDeleteFile(state))).Methods("DELETE")

	// put a file chunk
	r.Handle("/api/chunk/{fileid:[0-9]+}/{versionID:[0-9]+}/{chunknumber:[0-9]+}/{chunkhash}", authenticateToken(state, handlePutFileChunk(state))).Methods("PUT")

	// get a file chunk and returns the raw bytes of the encrypted chunk data
	r.Handle("/api/chunk/{fileid:[0-9]+}/{versionID:[0-9]+}/{chunknumber:[0-9]+}", authenticateToken(state, handleGetFileChunk(state))).Methods("GET")

	// get all known file chunks (except the chunks themselves)
	r.Handle("/api/chunk/{fileid:[0-9]+}/{versionID:[0-9]+}", authenticateToken(state, handleGetFileChunks(state))).Methods("GET")

	return r
}

// handleUsersLogin handles the incoming POST /api/users/login
func handleUsersLogin(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// get the user and password from the parameters
		err := r.ParseForm()
		if err != nil {
			http.Error(w, "Failed to parse form data for POST operation.", http.StatusBadRequest)
			return
		}

		vars := r.Form
		username := vars.Get("user")
		password := vars.Get("password")
		if username == "" || password == "" {
			http.Error(w, "Both user and password were not supplied.", http.StatusBadRequest)
			return
		}

		// check the username and password
		user, err := state.Authorizor.VerifyPassword(username, password)
		if err != nil || user == nil {
			http.Error(w, "Failed to log in with the data provided.", http.StatusBadRequest)
			return
		}

		// generate the authentication token
		token, err := state.Authorizor.GenerateToken(user.Name, user.ID)
		if err != nil {
			http.Error(w, "Failed to log in with the data provided.", http.StatusBadRequest)
			return
		}

		writeJSONResponse(w, &models.UserLoginResponse{
			Token:      token,
			CryptoHash: user.CryptoHash,
			Capabilities: models.ServerCapabilities{
				ChunkSize: *flagServeChunkSize,
			},
		})
	}
}

// handlePutUserCryptoHash updates a user's crypto hash which can be used to verify a
// client side entered password.
func handlePutUserCryptoHash(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// deserialize the JSON object that should be in the request body
		var req models.UserCryptoHashUpdateRequest
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read the request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err = json.Unmarshal(body, &req)
		if err != nil {
			http.Error(w, "Failed to parse the request as a JSON object: "+err.Error(), http.StatusBadRequest)
			return
		}

		// set the new crypto hash for the user
		err = state.Storage.UpdateUserCryptoHash(userCreds.ID, req.CryptoHash)
		if err != nil {
			http.Error(w, "Failed to update the user's crypto hash information for the authenticated user.", http.StatusInternalServerError)
			return
		}

		writeJSONResponse(w, &models.UserCryptoHashUpdateResponse{
			Status: true,
		})
	}
}

// handleGetUserStats returns a JSON object with the authenticated user's current
// stats susch as the quota, allocated byte count and current revision number.
func handleGetUserStats(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		stats, err := state.Storage.GetUserStats(userCreds.ID)
		if err != nil {
			http.Error(w, "Failed to get the user stats information for the authenticated user.", http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, &models.UserStatsGetResponse{
			Stats: filefreezer.UserStats{
				Quota:     stats.Quota,
				Allocated: stats.Allocated,
				Revision:  stats.Revision,
			},
		})
	}
}

// handleGetAllFiles returns a JSON object with all of the FileInfo objects in Storage
// that are bound to the user id authorized in the context of the call.
func handleGetAllFiles(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// pull down all the fileinfo objects for a user
		allFileInfos, err := state.Storage.GetAllUserFileInfos(userCreds.ID)
		if err != nil {
			http.Error(w, "Failed to get files for the user.", http.StatusNotFound)
			return
		}

		writeJSONResponse(w, &models.AllFilesGetResponse{
			Files: allFileInfos,
		})
	}
}

func handleNewFileVersion(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// deserialize the JSON object that should be in the request body
		var req models.NewFileVersionRequest
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read the request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err = json.Unmarshal(body, &req)
		if err != nil {
			http.Error(w, "Failed to parse the request as a JSON object: "+err.Error(), http.StatusBadRequest)
			return
		}

		// pull the file id from the URI matched by the mux
		vars := mux.Vars(r)
		fileID, err := strconv.ParseInt(vars["fileid"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the file id in the URI.", http.StatusBadRequest)
			return
		}

		// pull down the fileinfo object for a file ID
		fi, err := state.Storage.GetFileInfo(userCreds.ID, int(fileID))
		if err != nil {
			http.Error(w, "Failed to get file for the user.", http.StatusNotFound)
			return
		}

		// create new file version
		fi, err = state.Storage.TagNewFileVersion(userCreds.ID, int(fileID), req.Permissions, req.LastMod, req.ChunkCount, req.FileHash)
		if err != nil {
			http.Error(w, "Failed to tag a new version of the file for the user: "+err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSONResponse(w, &models.NewFileVersionResponse{
			FileInfo: *fi,
			Status:   true,
		})
	}
}

func handleGetAllFileVersion(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// pull the file id from the URI matched by the mux
		vars := mux.Vars(r)
		fileID, err := strconv.ParseInt(vars["fileid"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the file id in the URI.", http.StatusBadRequest)
			return
		}

		// pull down the fileinfo object for a file ID
		fi, err := state.Storage.GetFileInfo(userCreds.ID, int(fileID))
		if err != nil {
			http.Error(w, "Failed to get file for the user.", http.StatusNotFound)
			return
		}

		// get all the versions associated with the file in storage
		versionIDs, versionNums, err := state.Storage.GetFileVersions(fi.FileID)
		if err != nil {
			http.Error(w, "Failed to get file versions for the user.", http.StatusNotFound)
			return
		}

		writeJSONResponse(w, &models.FileGetAllVersionsResponse{
			VersionIDs:     versionIDs,
			VersionNumbers: versionNums,
		})
	}
}

// handleGetFile returns a JSON object with all of the FileInfo data for the file in Storage
// as well as a slice of missing chunks, if any.
func handleGetFile(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// pull the file id from the URI matched by the mux
		vars := mux.Vars(r)
		fileID, err := strconv.ParseInt(vars["fileid"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the file id in the URI.", http.StatusBadRequest)
			return
		}

		// pull down the fileinfo object for a file ID
		fi, err := state.Storage.GetFileInfo(userCreds.ID, int(fileID))
		if err != nil {
			http.Error(w, "Failed to get file for the user.", http.StatusNotFound)
			return
		}

		// get all of the missing chunks
		missingChunks, err := state.Storage.GetMissingChunkNumbersForFile(userCreds.ID, fi.FileID)
		if err != nil {
			http.Error(w, "Failed to get the missing chunks for the file.", http.StatusBadRequest)
			return
		}

		writeJSONResponse(w, &models.FileGetResponse{
			FileInfo:      *fi,
			MissingChunks: missingChunks,
		})
	}
}

// handlePutFileChunk reads a chunk from the request body and attempts to store it given the
// file ID, chunk number and hash supplied in parameters. A Status boolean is returned to
// indicate the success of the operation.
func handlePutFileChunk(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// pull the file id from the URI matched by the mux
		vars := mux.Vars(r)
		fileID, err := strconv.ParseInt(vars["fileid"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the file id in the URI.", http.StatusBadRequest)
			return
		}
		versionID, err := strconv.ParseInt(vars["versionID"], 10, 32)
		if err != nil {
			http.Error(w, "A valid string was not used for the version id in the URI.", http.StatusBadRequest)
			return
		}
		chunkNumber, err := strconv.ParseInt(vars["chunknumber"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the chunk number in the URI.", http.StatusBadRequest)
			return
		}
		chunkHash, okay := vars["chunkhash"]
		if !okay {
			http.Error(w, "A valid string was not used for the chunk hash in the URI.", http.StatusBadRequest)
			return
		}

		// get a byte limited reader, set to the maximum chunk size supported by Storage
		// plus a little extra space for cryptography information
		bodyReader := http.MaxBytesReader(w, r.Body, state.Storage.ChunkSize+128)
		defer bodyReader.Close()
		chunk, err := ioutil.ReadAll(bodyReader)
		if err != nil {
			http.Error(w, "Failed to read the chunk: "+err.Error(), http.StatusBadRequest)
			return
		}

		// AddFileChunk does verify that the user ID owns the fild ID so we don't need
		// to replicate that work here, just add the chunk.
		fc, err := state.Storage.AddFileChunk(userCreds.ID, int(fileID), int(versionID), int(chunkNumber), chunkHash, chunk)
		if err != nil || fc == nil {
			http.Error(w, "Failed to add the chunk to storage: "+err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSONResponse(w, &models.FileChunkPutResponse{
			Status: true,
		})
	}
}

// handleGetFile returns a JSON object with all of the FileInfo data for the file in Storage
// as well as a slice of missing chunks, if any.
func handleGetFileChunks(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// pull the file id from the URI matched by the mux
		vars := mux.Vars(r)
		fileID, err := strconv.ParseInt(vars["fileid"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the file id in the URI.", http.StatusBadRequest)
			return
		}
		versionID, err := strconv.ParseInt(vars["versionID"], 10, 32)
		if err != nil {
			http.Error(w, "A valid string was not used for the version id in the URI.", http.StatusBadRequest)
			return
		}

		chunks, err := state.Storage.GetFileChunkInfos(userCreds.ID, int(fileID), int(versionID))
		if err != nil {
			http.Error(w, "Failed to get the chunk informations for the file id in the URI.", http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, &models.FileChunksGetResponse{
			Chunks: chunks,
		})
	}
}

// handleGetFile returns a JSON object with all of the FileInfo data for the file in Storage
// as well as a slice of missing chunks, if any.
func handleGetFileChunk(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials.", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// pull the file id from the URI matched by the mux
		vars := mux.Vars(r)
		fileID, err := strconv.ParseInt(vars["fileid"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the file id in the URI.", http.StatusBadRequest)
			return
		}
		versionID, err := strconv.ParseInt(vars["versionID"], 10, 32)
		if err != nil {
			http.Error(w, "A valid string was not used for the version id in the URI.", http.StatusBadRequest)
			return
		}
		chunkNumber, err := strconv.ParseInt(vars["chunknumber"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the chunk number in the URI.", http.StatusBadRequest)
			return
		}

		// get the file info first to ensure ownership
		fi, err := state.Storage.GetFileInfo(userCreds.ID, int(fileID))
		if err != nil {
			http.Error(w, "Failed to get the file information for the file id in the URI.", http.StatusBadRequest)
			return
		}
		if fi.UserID != userCreds.ID {
			http.Error(w, "Access denied.", http.StatusForbidden)
			return
		}

		chunk, err := state.Storage.GetFileChunk(int(fileID), int(chunkNumber), int(versionID))
		if err != nil {
			http.Error(w, "Failed to get the chunk information for the file id and chunk number in the URI.", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(chunk.Chunk)))
		_, err = w.Write(chunk.Chunk)
		if err != nil {
			http.Error(w, "Failed to write the file chunk as a response.", http.StatusInternalServerError)
			return
		}
	}
}

// handlePutFile registers a file for a given user.
func handlePutFile(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// pull the user credentials
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// deserialize the JSON object that should be in the request body
		var req models.FilePutRequest
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read the request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err = json.Unmarshal(body, &req)
		if err != nil {
			http.Error(w, "Failed to parse the request as a JSON object: "+err.Error(), http.StatusBadRequest)
			return
		}

		// sanity check some input
		if len(req.FileName) < 1 {
			http.Error(w, "fileName must be supplied in the request", http.StatusBadRequest)
			return
		}
		if req.LastMod < 1 {
			http.Error(w, "lastMod time must be supplied in the request", http.StatusBadRequest)
			return
		}
		if req.ChunkCount < 0 {
			http.Error(w, "chunkCount must be supplied in the request", http.StatusBadRequest)
			return
		}
		if len(req.FileHash) < 1 {
			http.Error(w, "fileHash must be supplied in the request", http.StatusBadRequest)
			return
		}

		// register a new file in storage with the information
		fi, err := state.Storage.AddFileInfo(userCreds.ID, req.FileName, req.IsDir, req.Permissions, req.LastMod, req.ChunkCount, req.FileHash)
		if err != nil {
			http.Error(w, "Failed to put a new file in storage for the user. "+err.Error(), http.StatusConflict)
			return
		}

		writeJSONResponse(w, &models.FilePutResponse{
			FileInfo: *fi,
		})
	}
}

func handleDeleteFile(state *serverState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// pull the user credentials
		ctx := r.Context()
		userCredsI := ctx.Value(userCredentialsContextKey("UserCredentials"))
		if userCredsI == nil {
			http.Error(w, "Failed to get the user credentials", http.StatusUnauthorized)
			return
		}
		userCreds := userCredsI.(*userCredentialsContext)

		// pull the file id from the URI matched by the mux
		vars := mux.Vars(r)
		fileID, err := strconv.ParseInt(vars["fileid"], 10, 32)
		if err != nil {
			http.Error(w, "A valid integer was not used for the file id in the URI.", http.StatusBadRequest)
			return
		}

		// delete a file from storage with the information
		err = state.Storage.RemoveFile(userCreds.ID, int(fileID))
		if err != nil {
			http.Error(w, "Failed to remove a file in storage for the user. "+err.Error(), http.StatusConflict)
			return
		}

		writeJSONResponse(w, &models.FileDeleteResponse{Success: true})
	}
}

type userCredentialsContextKey string
type userCredentialsContext struct {
	ID   int
	Name string
}

// authenticateToken middleware calls out to the auth module to authenticate
// the token contained in the header of the response to ensure user credentials
// before calling the next handler.
func authenticateToken(state *serverState, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// validate the token
		token, err := state.Authorizor.VerifyToken(r)
		if err != nil || token == nil {
			http.Error(w, "Failed to authenticate.", http.StatusForbidden)
			return
		}
		username, userid := state.Authorizor.GetUserFromToken(token)
		creds := &userCredentialsContext{userid, username}

		// authenticated, so proceed to next handler
		ctx := r.Context()
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, userCredentialsContextKey("UserCredentials"), creds)))
	})
}

// writeJSONResponse marshals the generic data object into JSON and then
// writes it out to the ResponseWriter. If the marshalling fails, then
// a 500 response is returned with the error message.
func writeJSONResponse(w http.ResponseWriter, data interface{}) {
	// set the response to be JSON
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// marshal the data
	json, err := json.Marshal(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// write it out
	w.Write(json)
}
