// Copyright 2017, Timothy Bogdala <tdb@animal-machine.com>
// See the LICENSE file for more details.

package main

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/tbogdala/filefreezer"
	"github.com/tbogdala/filefreezer/cmd/freezer/models"

	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

// User kingpin to define a set of commands and flags for the application.
var (
	appFlags           = kingpin.New("freezer", "A web application server for FileFreezer.")
	flagDatabasePath   = appFlags.Flag("db", "The database path.").Default("file:freezer.db").String()
	flagPublicKeyPath  = appFlags.Flag("pub", "The file path to the public key.").Default("freezer.rsa.pub").String()
	flagPrivateKeyPath = appFlags.Flag("priv", "The file path to the private key.").Default("freezer.rsa").String()
	flagTLSKey         = appFlags.Flag("tlskey", "The HTTPS TLS private key file.").Default("freezer.key").String()
	flagTLSCrt         = appFlags.Flag("tlscert", "The HTTPS TLS public crt file.").Default("freezer.crt").String()
	flagChunkSize      = appFlags.Flag("cs", "The number of bytes contained in one chunk.").Default("4194304").Int64() // 4 MB
	flagExtraStrict    = appFlags.Flag("xs", "File checking should be extra strict on file sync comparisons.").Default("true").Bool()

	cmdServe      = appFlags.Command("serve", "Adds a new user to the storage.")
	argListenAddr = cmdServe.Arg("http", "The net address to listen to").Default(":8080").String()

	cmdAddUser      = appFlags.Command("adduser", "Adds a new user to the storage.")
	argAddUserName  = cmdAddUser.Arg("username", "The username for user.").Required().String()
	argAddUserPass  = cmdAddUser.Arg("password", "The password for user.").Required().String()
	argAddUserQuota = cmdAddUser.Arg("quota", "The quota size in bytes.").Default("1000000000").Int()

	cmdModUser         = appFlags.Command("moduser", "Modifies a user in storage.")
	argModUserName     = cmdModUser.Arg("username", "The username for existing user.").Required().String()
	argModUserNewQuota = cmdModUser.Flag("quota", "New quota size in bytes.").Short('q').Int()
	argModUserNewName  = cmdModUser.Flag("user", "New quota size in bytes.").Short('u').String()
	argModUserNewPass  = cmdModUser.Flag("pass", "New quota size in bytes.").Short('p').String()

	cmdUserStats     = appFlags.Command("userstats", "Gets the quota, allocation and revision counts for the user.")
	argUserStatsHost = cmdUserStats.Arg("hostname", "The host URI for the storage server to contact.").Required().String()
	argUserStatsName = cmdUserStats.Arg("username", "The username for user.").Required().String()
	argUserStatsPass = cmdUserStats.Arg("password", "The password for user.").Required().String()

	cmdGetFiles     = appFlags.Command("getfiles", "Gets all files for a user in storage.")
	argGetFilesHost = cmdGetFiles.Arg("hostname", "The host URI for the storage server to contact.").Required().String()
	argGetFilesName = cmdGetFiles.Arg("username", "The username for user.").Required().String()
	argGetFilesPass = cmdGetFiles.Arg("password", "The password for user.").Required().String()

	cmdAddFile       = appFlags.Command("addfile", "Put a file into storage.")
	argAddFileHost   = cmdAddFile.Arg("hostname", "The host URI for the storage server to contact.").Required().String()
	argAddFileName   = cmdAddFile.Arg("username", "The username for user.").Required().String()
	argAddFilePass   = cmdAddFile.Arg("password", "The password for user.").Required().String()
	argAddFilePath   = cmdAddFile.Arg("filename", "The local file to put on the server.").Required().String()
	argAddFileTarget = cmdAddFile.Arg("target", "The file path to use on the server for the local file; defaults to the same as the filename arg.").Default("").String()

	cmdRmFile     = appFlags.Command("rmfile", "Remove a file from storage.")
	argRmFileHost = cmdRmFile.Arg("hostname", "The host URI for the storage server to contact.").Required().String()
	argRmFileName = cmdRmFile.Arg("username", "The username for user.").Required().String()
	argRmFilePass = cmdRmFile.Arg("password", "The password for user.").Required().String()
	argRmFilePath = cmdRmFile.Arg("filename", "The file to remove on the server.").Required().String()

	cmdSync       = appFlags.Command("sync", "Synchronizes a path with the server.")
	argSyncHost   = cmdSync.Arg("hostname", "The host URI for the storage server to contact.").Required().String()
	argSyncName   = cmdSync.Arg("username", "The username for user.").Required().String()
	argSyncPass   = cmdSync.Arg("password", "The password for user.").Required().String()
	argSyncPath   = cmdSync.Arg("filepath", "The file to sync with the server.").Required().String()
	argSyncTarget = cmdSync.Arg("target", "The file path to sync to on the server; defaults to the same as the filename arg.").Default("").String()
)

// openStorage is the common function used to open the filefreezer Storage
func openStorage() (*filefreezer.Storage, error) {
	log.Printf("Opening database: %s\n", *flagDatabasePath)

	// open up the storage database
	store, err := filefreezer.NewStorage(*flagDatabasePath)
	if err != nil {
		return nil, err
	}
	store.ChunkSize = *flagChunkSize
	store.CreateTables()
	return store, nil
}

func main() {
	switch kingpin.MustParse(appFlags.Parse(os.Args[1:])) {
	case cmdServe.FullCommand():
		// setup a new server state or exit out on failure
		state, err := newState()
		if err != nil {
			log.Fatalf("Unable to initialize the server: %v", err)
		}
		defer closeState(state)

		runServe(state, nil)

	case cmdAddUser.FullCommand():
		username := *argAddUserName
		password := *argAddUserPass
		quota := *argAddUserQuota
		store, err := openStorage()
		if err != nil {
			log.Fatalf("Failed to open the storage database: %v", err)
		}
		runAddUser(store, username, password, quota)

	case cmdModUser.FullCommand():
		store, err := openStorage()
		if err != nil {
			log.Fatalf("Failed to open the storage database: %v", err)
		}
		runModUser(store, *argModUserName, *argModUserNewQuota, *argModUserNewName, *argModUserNewPass)

	case cmdGetFiles.FullCommand():
		target := *argGetFilesHost
		username := *argGetFilesName
		password := *argGetFilesPass
		authToken, err := runUserAuthenticate(target, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", target, err)
		}
		allFiles, err := runGetAllFileHashes(target, authToken)
		if err != nil {
			log.Fatalf("Failed to get all of the files for the user %s from the storage server %s: %v", username, target, err)
		}

		// TODO: Better formmating
		log.Printf("All files: %v", allFiles)

	case cmdAddFile.FullCommand():
		target := *argAddFileHost
		username := *argAddFileName
		password := *argAddFilePass
		filepath := *argAddFilePath
		remoteTarget := *argAddFileTarget
		if len(remoteTarget) < 1 {
			remoteTarget = filepath
		}
		authToken, err := runUserAuthenticate(target, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", target, err)
		}

		data, err := calcFileHashInfo(*flagChunkSize, filepath)
		if err != nil {
			log.Fatalf("Failed to calculate the required data for the file %s: %v", filepath, err)
		}

		fileID, err := runAddFile(target, authToken, filepath, remoteTarget, data.LastMod, data.ChunkCount, data.Hash)
		if err != nil {
			log.Fatalf("Failed to register the file on the server %s: %v", target, err)
		}

		log.Printf("File added (id: %d): %s\n", fileID, filepath)

	case cmdRmFile.FullCommand():
		target := *argRmFileHost
		username := *argRmFileName
		password := *argRmFilePass
		filepath := *argRmFilePath

		authToken, err := runUserAuthenticate(target, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", target, err)
		}

		err = runRmFile(target, authToken, filepath)
		if err != nil {
			log.Fatalf("Failed to remove file from the server %s: %v", target, err)
		}

	case cmdSync.FullCommand():
		target := *argSyncHost
		username := *argSyncName
		password := *argSyncPass
		filepath := *argSyncPath
		remoteFilepath := *argSyncTarget
		if len(remoteFilepath) < 1 {
			remoteFilepath = filepath
		}
		authToken, err := runUserAuthenticate(target, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", target, err)
		}

		_, _, err = runSyncFile(target, authToken, filepath, remoteFilepath)
		if err != nil {
			log.Fatalf("Failed to synchronize the path %s: %v", filepath, err)
		}

	case cmdUserStats.FullCommand():
		target := *argUserStatsHost
		username := *argUserStatsName
		password := *argUserStatsPass

		authToken, err := runUserAuthenticate(target, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", target, err)
		}

		_, err = runUserStats(target, authToken)
		if err != nil {
			log.Fatalf("Failed to get the user stats from the server %s: %v", target, err)
		}

	}
}

// fileHashData encapsulates return data for file hash calculation.
type fileHashData struct {
	Hash       string
	LastMod    int64
	ChunkCount int
}

// calcFileHashInfo calculates the file hash as well as pulling useful information such as
// last modified time and chunk count required.
func calcFileHashInfo(maxChunkSize int64, filename string) (*fileHashData, error) {
	data := new(fileHashData)

	fileInfo, err := os.Stat(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to stat the local file (%s) for the test", filename)
	}

	data.LastMod = fileInfo.ModTime().UTC().Unix()

	// calculate the chunk count required for the file size
	fileSize := fileInfo.Size()
	data.ChunkCount = int((fileSize - (fileSize % maxChunkSize) + maxChunkSize) / maxChunkSize)

	// generate a hash for the test file
	hasher := sha1.New()
	fileBytes, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create a file byte array for the hashing operation: %v", err)
	}
	hasher.Write(fileBytes)
	hash := hasher.Sum(nil)
	data.Hash = base64.URLEncoding.EncodeToString(hash)

	return data, nil
}

// closeState will close any state connections used by the server
func closeState(s *models.State) {
	s.Storage.Close()
}

// newState does the setup for the initial state of the server
func newState() (*models.State, error) {
	s := new(models.State)
	s.PrivateKeyPath = *flagPrivateKeyPath
	s.PublicKeyPath = *flagPublicKeyPath
	s.DatabasePath = *flagDatabasePath

	// load the private key
	var err error
	s.SignKey, err = ioutil.ReadFile(s.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to read the private key (%s). %v", s.PrivateKeyPath, err)
	}

	// load the public key
	s.VerifyKey, err = ioutil.ReadFile(s.PublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to read the public key (%s). %v", s.PublicKeyPath, err)
	}

	// attempt to open the storage database
	s.Storage, err = openStorage()
	if err != nil {
		return nil, fmt.Errorf("Failed to open the database using the path specified (%s): %v", s.DatabasePath, err)
	}

	// assign the token generator
	s.Authorizor, err = NewJWTAuthenticator(s.Storage, s.SignKey, s.VerifyKey)
	if err != nil {
		return nil, fmt.Errorf("Failed to create the JWT token generator: %v", err)
	}

	log.Printf("Database opened: %s\n", s.DatabasePath)
	return s, nil
}
