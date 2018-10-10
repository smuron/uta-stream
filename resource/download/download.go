package download

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"

	shell "github.com/ipfs/go-ipfs-api"
	"github.com/rylio/ytdl"
)

var knownProviders = [...]string{"youtube.com", "youtu.be"}
var tempDLFolder = "TEMP-DL"

// Master download router. Looks at the url and determins which service needs
// to hand the url
func Download(rawurl string, ipfs *shell.Shell, removeMp4 bool) (ipfsPath string, err error) {
	// Ensure the temporary directory for storing downloads exists
	if _, err = os.Stat(tempDLFolder); os.IsNotExist(err) {
		os.Mkdir(tempDLFolder, os.ModePerm)
	}

	// Parse the URL
	urlToDL, err := url.Parse(rawurl)
	if err != nil {
		// TODO: Eventually do a text-search of youtube and just DL top result
		return "", err
	}

	// www causes things to catch on fire
	if urlToDL.Hostname() == "www.youtube.com" {
		urlToDL.Host = "youtube.com"
	}

	// Route to different handlers based on hostname
	switch urlToDL.Hostname() {
	case "youtube.com", "youtu.be":
		return downloadYoutube(*urlToDL, ipfs, removeMp4)
	default:
		// TODO: Eventually do a text-search of youtube and just DL top result
		return "", fmt.Errorf("URL hostname (%v) doesn't match a known provider.\n"+
			"Should be one of: %v\n", urlToDL.Hostname(), knownProviders)
	}
}

func downloadYoutube(urlToDL url.URL, ipfs *shell.Shell, removeMp4 bool) (ipfsPath string, err error) {
	// Get the info for the video
	var vidInfo *ytdl.VideoInfo
	switch urlToDL.Hostname() {
	case "youtube.com":
		vidInfo, err = ytdl.GetVideoInfoFromURL(&urlToDL)
	case "youtu.be":
		vidInfo, err = ytdl.GetVideoInfoFromShortURL(&urlToDL)
	}

	if err != nil {
		return "", fmt.Errorf("failed to fetch provided Youtube url. Err: %v", err)
	}

	// Figure out the highest bitrate format
	formats := vidInfo.Formats
	bestFormat := formats.Best(ytdl.FormatAudioBitrateKey)[0] // Format with highest bitrate

	// Download the mp4
	log.Printf("Downloading mp4 from %v\n", urlToDL.EscapedPath())
	fileLocation := filepath.Join(tempDLFolder, vidInfo.Title)
	_ = os.MkdirAll(filepath.Dir(fileLocation), os.ModePerm)
	file, err := os.Create(fileLocation + ".mp4")
	if err != nil {
		return "", fmt.Errorf("failed to create mp4 file. Err: %v", err)
	}
	vidInfo.Download(bestFormat, file)
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("failed to write mp4. Err: %v", err)
	}
	log.Printf("Downloading of %v complete\n", urlToDL.EscapedPath())

	// Extract the audio part of the mp4
	mp3, err := splitAudio(fileLocation, removeMp4)
	if err != nil {
		return "", err
	}

	// Add to IPFS
	mp3File, err := os.Open(mp3)
	if err != nil {
		return "", fmt.Errorf("Failed to open downloaded mp3. Err: %v\n", err)
	}
	ipfsPath, err = ipfs.Add(mp3File)
	if err != nil {
		return "", fmt.Errorf("Failed to add to IPFS. Err: %v\n", err)
	}
	if err = mp3File.Close(); err != nil {
		return "", fmt.Errorf("failed to close mp3 after ipfs write. Err: %v", err)
	}

	// Remove the mp3 now that we've added
	if err = os.Remove(mp3); err != nil {
		log.Printf("Failed to remove mp3. Err: %v\n", err)
	}

	return ipfsPath, nil
}

// Given an mp4 extract the audio into an mp3
// Remove mp4 will try to delete the mp4 once conversion is done
// Failture to delete the mp4 will only result in a log, not an error
// Requires ffmpeg to be in PATH
func splitAudio(fileLocation string, removeMp4 bool) (string, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	var mp3 string
	if err != nil {
		return "", fmt.Errorf("ffmpeg was not found in PATH. Please install ffmpeg")
	} else {
		log.Printf("Attempting to isolate audio as mp3 from %v\n", fileLocation+".mp4")
		fname := fileLocation + ".mp4"
		mp3 = fname + ".mp3"
		cmd := exec.Command(ffmpeg, "-y", "-loglevel", "quiet", "-i", fname, "-vn", mp3)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			fmt.Println("Failed to extract audio:", err)
			return "", err
		} else {
			fmt.Println()
			fmt.Println("Extracted audio:", mp3)
			if removeMp4 {
				if err = os.Remove(fileLocation); err != nil {
					log.Printf("Failed to remove mp4. Err: %v\n", err)
				}
			}
		}
	}

	return mp3, nil
}
