// Copyright 2017, Timothy Bogdala <tdb@animal-machine.com>
// See the LICENSE file for more details.

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"math/rand"
	"os"
	"runtime/pprof"
	"time"

	"github.com/tbogdala/filefreezer"
	"github.com/tbogdala/filefreezer/cmd/freezer/command"

	"strings"

	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

// User kingpin to define a set of commands and flags for the application.
var (
	appFlags         = kingpin.New("freezer", "A command-line interface to filefreezer able to act as client or server.")
	flagDatabasePath = appFlags.Flag("db", "The database path.").Default("file:freezer.db").String()
	flagTLSKey       = appFlags.Flag("tlskey", "The HTTPS TLS private key file.").String()
	flagTLSCrt       = appFlags.Flag("tlscert", "The HTTPS TLS public crt file.").String()
	flagExtraStrict  = appFlags.Flag("xs", "File checking should be extra strict on file sync comparisons.").Default("true").Bool()
	flagUserName     = appFlags.Flag("user", "The username for user.").Short('u').String()
	flagUserPass     = appFlags.Flag("pass", "The password for user.").Short('p').String()
	flagCryptoPass   = appFlags.Flag("crypt", "The passwod used for cryptography.").Short('s').String()
	flagHost         = appFlags.Flag("host", "The host URL for the server to contact.").Short('h').String()
	flagCPUProfile   = appFlags.Flag("cpuprofile", "Turns on cpu profiling and stores the result in the file specified by this flag.").String()
	flagQuiet        = appFlags.Flag("quiet", "Turns off non-fatal error console output for the command.").Bool()

	cmdServe           = appFlags.Command("serve", "Adds a new user to the storage.")
	argServeListenAddr = cmdServe.Arg("http", "The net address to listen to").Default(":8080").String()
	flagServeChunkSize = cmdServe.Flag("cs", "The number of bytes contained in one chunk.").Default("4194304").Int64() // 4 MB

	cmdAddUser       = appFlags.Command("adduser", "Adds a new user to the storage.")
	flagAddUserQuota = cmdAddUser.Flag("newquota", "The quota size in bytes.").Short('q').Default("1000000000").Int()

	cmdRmUser = appFlags.Command("rmuser", "Removes a user from the storage.")

	cmdModUser          = appFlags.Command("moduser", "Modifies a user in storage.")
	flagModUserNewQuota = cmdModUser.Flag("newquota", "New quota size in bytes.").Short('Q').Int()
	flagModUserNewName  = cmdModUser.Flag("newuser", "New quota size in bytes.").Short('U').String()
	flagModUserNewPass  = cmdModUser.Flag("newpass", "New quota size in bytes.").Short('P').String()

	cmdUserStats = appFlags.Command("userstats", "Gets the quota, allocation and revision counts for the user.")

	cmdGetFiles = appFlags.Command("getfiles", "Gets all files for a user in storage.")

	cmdCrypto           = appFlags.Command("crypto", "Cryptography configuration and operation.")
	cmdCryptoSetPass    = cmdCrypto.Command("setpass", "Sets the cryptography password to use for files being synced.")
	flagCryptoSetPassPW = cmdCryptoSetPass.Arg("newpass", "New cryptography password.").String()

	cmdGetFileVersions       = appFlags.Command("versions", "Gets all file versions for a given file in storage.")
	argGetFileVersionsTarget = cmdGetFileVersions.Arg("target", "The file path to on the server to get version information for.").String()

	cmdRmFile     = appFlags.Command("rmfile", "Remove a file from storage.")
	argRmFilePath = cmdRmFile.Arg("filename", "The file to remove on the server.").Required().String()

	cmdSync       = appFlags.Command("sync", "Synchronizes a path with the server.")
	argSyncPath   = cmdSync.Arg("filepath", "The file to sync with the server.").Required().String()
	argSyncTarget = cmdSync.Arg("target", "The file path to sync to on the server; defaults to the same as the filename arg.").Default("").String()

	cmdSyncDir       = appFlags.Command("syncdir", "Synchronizes a directory with the server.")
	argSyncDirPath   = cmdSyncDir.Arg("dirpath", "The directory to sync with the server.").Required().String()
	argSyncDirTarget = cmdSyncDir.Arg("target", "The directory path to sync to on the server; defaults to the same as the filename arg.").Default("").String()
)

func logPrintln(v ...interface{}) {
	if *flagQuiet {
		return
	}

	log.Println(v...)
}

func logPrintf(format string, v ...interface{}) {
	if *flagQuiet {
		return
	}

	log.Printf(format, v...)
}

func fmtPrintln(v ...interface{}) {
	if *flagQuiet {
		return
	}

	fmt.Println(v...)
}

func fmtPrintf(format string, v ...interface{}) {
	if *flagQuiet {
		return
	}

	fmt.Printf(format, v...)
}

// openStorage is the common function used to open the filefreezer Storage
func openStorage() (*filefreezer.Storage, error) {
	logPrintf("Opening database: %s\n", *flagDatabasePath)

	// open up the storage database
	store, err := filefreezer.NewStorage(*flagDatabasePath)
	if err != nil {
		return nil, err
	}
	store.CreateTables()
	return store, nil
}

func interactiveGetLoginUser() string {
	if *flagUserName != "" {
		return *flagUserName
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Username: ")
	username, _ := reader.ReadString('\n')
	return strings.TrimSpace(username)
}

func interactiveGetLoginPassword() string {
	if *flagUserPass != "" {
		return *flagUserPass
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Password: ")
	//fmtPrintln("\033[8m") // Hide input
	password, _ := reader.ReadString('\n')
	//fmtPrintln("\033[28m") // Show input

	return strings.TrimSpace(password)
}

func interactiveGetCryptoPassword() string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Cryptography password: ")
	//fmtPrintln("\033[8m") // Hide input
	password, _ := reader.ReadString('\n')
	//fmtPrintln("\033[28m") // Show input

	return strings.TrimSpace(password)
}

// initCrypto makes sure that the crypto hash has been setup
// for the user. if the user authenticated and a crypto hash was not returned
// in the reply, this function prompts the user for the password and makes
// the call to the server to set the crypto hash. after the crypto hash is
// ensured to exist, the crypto key is derived from the crypto password and
// verified against this hash. an error is returned on failure.
// note: this should only be run after command.State.authenticate().
func initCrypto(cmdState *command.State) error {
	// if a crypto hash has not been setup already, do so now
	if len(cmdState.CryptoHash) == 0 {
		newPassword := interactiveFirstTimeSetCryptoPassword()
		err := cmdState.SetCryptoHashForPassword(newPassword)
		if err != nil {
			return err
		}

		*flagCryptoPass = newPassword
	}

	if *flagCryptoPass == "" {
		*flagCryptoPass = interactiveGetCryptoPassword()
	}

	// check the crypto password against the stored hash of the key and keep
	// the resulting crypto key if the verification was successful.
	var err error
	cmdState.CryptoKey, err = filefreezer.VerifyCryptoPassword(*flagCryptoPass, string(cmdState.CryptoHash))
	if err != nil {
		return err
	}

	if cmdState.CryptoKey == nil {
		return fmt.Errorf("the cryptography password supplied is invalid")
	}

	return nil
}

func interactiveFirstTimeSetCryptoPassword() string {
	reader := bufio.NewReader(os.Stdin)
	fmtPrintln("The cryptography password has not been set for this account.")
	fmtPrintln("Filefreezer will encrypt all data before sending it to the server, but")
	fmtPrintln("it needs a password to encrypt with. Please enter a secure passphrase")
	fmtPrintln("below, but keep in mind that the software will have no way of recovering")
	fmtPrintln("encrypted data from the server if this password is lost.")

	var password1, password2 string
	verified := false
	for !verified {
		fmtPrintln("")
		fmt.Print("Cryptography password: ")
		//fmtPrintln("\033[8m") // Hide input
		password1, _ = reader.ReadString('\n')
		password1 = strings.TrimSpace(password1)
		//fmtPrintln("\033[28m") // Show input

		// special sanity check to avoid empty passwords
		if password1 == "" {
			fmtPrintln("An empty cryptography password cannot be used!")
			continue
		}

		fmt.Print("Verify cryptography password: ")
		//fmtPrintln("\033[8m") // Hide inputde
		password2, _ = reader.ReadString('\n')
		password2 = strings.TrimSpace(password2)
		//fmtPrintln("\033[28m") // Show input

		// make sure the user entered the same password twice
		if strings.Compare(password1, password2) == 0 {
			verified = true
		} else {
			fmtPrintln("Cryptography passwords did not match. Try again.")
		}
	}

	return password1
}

func interactiveGetHost() string {
	var host string

	if *flagHost != "" {
		host = *flagHost
	} else {
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Server URL: ")
		host, _ = reader.ReadString('\n')
	}

	host = strings.TrimSpace(host)

	// ensure the host string has a protocol prefix
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}

	return host
}

func main() {
	parsedFlags := kingpin.MustParse(appFlags.Parse(os.Args[1:]))
	rand.Seed(time.Now().UnixNano())

	cmdState := command.NewState()
	cmdState.TLSKey = *flagTLSKey
	cmdState.TLSCrt = *flagTLSCrt
	cmdState.ExtraStrict = *flagExtraStrict
	if *flagQuiet {
		cmdState.SetQuiet(true)
	}

	cmdState.Println("Filefreezer Copyright (C) 2017 by Timothy Bogdala <tdb@animal-machine.com>")
	cmdState.Println("This program comes with ABSOLUTELY NO WARRANTY. This is free software")
	cmdState.Println("and you are welcome to redistribute it under certain conditions.")
	cmdState.Println("")

	// potentially enable cpu profiling
	if *flagCPUProfile != "" {
		cmdState.Printf("Enabling CPU Profiling!\n")
		cpuPprofF, err := os.Create(*flagCPUProfile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(cpuPprofF)
		defer func() {
			pprof.StopCPUProfile()
			cpuPprofF.Close()
		}()
	}

	switch parsedFlags {
	case cmdServe.FullCommand():
		// setup a new server state or exit out on failure
		state, err := newState()
		if err != nil {
			log.Fatalf("Unable to initialize the server: %v", err)
		}
		defer state.close()
		state.Storage.ChunkSize = *flagServeChunkSize
		quitCh := state.serve(nil)

		// wait until server shutdown to Exit out
		for {
			select {
			case <-quitCh:
				os.Exit(0)
			}
		}

	case cmdAddUser.FullCommand():
		store, err := openStorage()
		if err != nil {
			log.Fatalf("Failed to open the storage database: %v", err)
		}
		username := interactiveGetLoginUser()
		password := interactiveGetLoginPassword()
		cmdState.AddUser(store, username, password, *flagAddUserQuota)

	case cmdRmUser.FullCommand():
		store, err := openStorage()
		if err != nil {
			log.Fatalf("Failed to open the storage database: %v", err)
		}
		username := interactiveGetLoginUser()
		cmdState.RmUser(store, username)

	case cmdModUser.FullCommand():
		store, err := openStorage()
		if err != nil {
			log.Fatalf("Failed to open the storage database: %v", err)
		}
		username := interactiveGetLoginUser()
		cmdState.ModUser(store, username, *flagModUserNewQuota, *flagModUserNewName, *flagModUserNewPass)

	case cmdCryptoSetPass.FullCommand():
		username := interactiveGetLoginUser()
		password := interactiveGetLoginPassword()
		host := interactiveGetHost()

		if *flagCryptoSetPassPW == "" {
			*flagCryptoSetPassPW = interactiveGetCryptoPassword()
		}

		err := cmdState.Authenticate(host, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", host, err)
		}

		cmdState.SetCryptoHashForPassword(*flagCryptoSetPassPW)

	case cmdGetFiles.FullCommand():
		username := interactiveGetLoginUser()
		password := interactiveGetLoginPassword()
		host := interactiveGetHost()

		err := cmdState.Authenticate(host, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", host, err)
		}

		err = initCrypto(cmdState)
		if err != nil {
			log.Fatalf("Failed to initialize cryptography: %v", err)
		}

		allFiles, err := cmdState.GetAllFileHashes()
		if err != nil {
			log.Fatalf("Failed to get all of the files for the user %s from the storage server %s: %v", username, host, err)
		}

		logPrintf("Registered files for %s:\n", username)
		logPrintln(strings.Repeat("=", 22+len(username)))
		logPrintln("FileID   | VerNum   | Flags    | Filename")
		logPrintln(strings.Repeat("-", 41))

		var builder bytes.Buffer
		for _, fi := range allFiles {
			builder.Reset()
			builder.WriteString(fmt.Sprintf("%08d | %08d | ", fi.FileID, fi.CurrentVersion.VersionNumber))
			if fi.IsDir {
				builder.WriteString("D        | ")
			} else {
				builder.WriteString("F        | ")
			}

			decryptedFilename, err := cmdState.DecryptString(fi.FileName)
			if err != nil {
				logPrintf("Failed to decrypt filename for file id %d: %v", fi.FileID, err)
			}

			builder.WriteString(fmt.Sprintf("%s", decryptedFilename))
			logPrintln(builder.String())
		}

	case cmdGetFileVersions.FullCommand():
		username := interactiveGetLoginUser()
		password := interactiveGetLoginPassword()
		host := interactiveGetHost()

		err := cmdState.Authenticate(host, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", host, err)
		}

		err = initCrypto(cmdState)
		if err != nil {
			log.Fatalf("Failed to initialize cryptography: %v", err)
		}

		_, _, err = cmdState.GetFileVersions(*argGetFileVersionsTarget)
		if err != nil {
			log.Fatalf("Failed to get the file versions for the user %s from the storage server %s: %v", username, host, err)
		}

	case cmdRmFile.FullCommand():
		username := interactiveGetLoginUser()
		password := interactiveGetLoginPassword()
		host := interactiveGetHost()

		err := cmdState.Authenticate(host, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", host, err)
		}

		err = initCrypto(cmdState)
		if err != nil {
			log.Fatalf("Failed to initialize cryptography: %v", err)
		}

		filepath := *argRmFilePath
		err = cmdState.RmFile(filepath)
		if err != nil {
			log.Fatalf("Failed to remove file from the server %s: %v", host, err)
		}

	case cmdSync.FullCommand():
		username := interactiveGetLoginUser()
		password := interactiveGetLoginPassword()
		host := interactiveGetHost()

		err := cmdState.Authenticate(host, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", host, err)
		}

		err = initCrypto(cmdState)
		if err != nil {
			log.Fatalf("Failed to initialize cryptography: %v", err)
		}

		filepath := *argSyncPath
		remoteFilepath := *argSyncTarget
		if len(remoteFilepath) < 1 {
			remoteFilepath = filepath
		}
		_, _, err = cmdState.SyncFile(filepath, remoteFilepath)
		if err != nil {
			log.Fatalf("Failed to synchronize the path %s: %v", filepath, err)
		}

	case cmdSyncDir.FullCommand():
		username := interactiveGetLoginUser()
		password := interactiveGetLoginPassword()
		host := interactiveGetHost()

		err := cmdState.Authenticate(host, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", host, err)
		}

		err = initCrypto(cmdState)
		if err != nil {
			log.Fatalf("Failed to initialize cryptography: %v", err)
		}

		filepath := *argSyncDirPath
		remoteFilepath := *argSyncDirTarget
		if len(remoteFilepath) < 1 {
			remoteFilepath = filepath
		}
		_, err = cmdState.SyncDirectory(filepath, remoteFilepath)
		if err != nil {
			log.Fatalf("Failed to synchronize the directory %s: %v", filepath, err)
		}

	case cmdUserStats.FullCommand():
		username := interactiveGetLoginUser()
		password := interactiveGetLoginPassword()
		host := interactiveGetHost()

		err := cmdState.Authenticate(host, username, password)
		if err != nil {
			log.Fatalf("Failed to authenticate to the server %s: %v", host, err)
		}

		_, err = cmdState.GetUserStats()
		if err != nil {
			log.Fatalf("Failed to get the user stats from the server %s: %v", host, err)
		}

	}
}
