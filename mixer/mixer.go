package mixer

import (
	"io"
	"log"
	"sync"
	"time"

	"github.com/VivaLaPanda/uta-stream/encoder"
	"github.com/VivaLaPanda/uta-stream/queue"
	"github.com/VivaLaPanda/uta-stream/resource/cache"
)

// Mixer is in charge of interacting with the queue and resource cache
// to go from a queue of song hashes to a stream of audio data. It has
// logic to ensure minimal delay between songs by processing current/next
// in parallel. Mixer can be considered the key component that ties together
// all the rest
type Mixer struct {
	Output           chan []byte
	packetsPerSecond int
	currentSong      *chan []byte
	nextSong         *chan []byte
	queue            *queue.Queue
	cache            *cache.Cache
	currentSongPath  string
	nextSongPath     string
	playLock         *sync.Mutex
}

// Bigger packet buffer means more resiliance but may cause
// strange behavior when skipping a song. In my experience a small value is best
var packetBufferSize = 8

// Packets-per-second sacrifices reliability for synchronization
// Higher means more synchornized streams. Minimum should be 1, super large
// values have undefined behaviour
// 2 is a reasonable default
func NewMixer(queue *queue.Queue, cache *cache.Cache, packetsPerSecond int) *Mixer {
	currentSong := make(chan []byte, packetBufferSize)
	nextSong := make(chan []byte, packetBufferSize)
	mixer := &Mixer{
		Output:           make(chan []byte, packetBufferSize),
		packetsPerSecond: packetsPerSecond,
		currentSong:      &currentSong,
		nextSong:         &nextSong,
		queue:            queue,
		cache:            cache,
		currentSongPath:  "",
		nextSongPath:     "",
		playLock:         &sync.Mutex{}}

	// Spin up the job to cast from the current song to our output
	// and handle song transitions
	go func() {
		for true {
			var broadcastPacket []byte

			if len(*mixer.currentSong) != 0 {
				broadcastPacket = <-*mixer.currentSong
				// We can succesfully read from the current song, all is good
			} else if len(*mixer.nextSong) != 0 {
				broadcastPacket = <-*mixer.nextSong
				// We couldn't play from current, assume that the song ended
				mixer.queue.NotifyDone(mixer.currentSongPath)
				mixer.currentSong = mixer.nextSong
				mixer.currentSongPath = mixer.nextSongPath
				tempSong, tempPath, isEmpty := mixer.fetchNextSong()
				if !isEmpty {
					mixer.nextSong = tempSong
					mixer.nextSongPath = tempPath
				}
			} else {
				// Both are empty, we really have nothing to do. Wait 10 seconds and try
				// again
				tempSong, tempPath, isEmpty := mixer.fetchNextSong()
				if !isEmpty {
					mixer.currentSong = tempSong
					mixer.currentSongPath = tempPath
				}
				// This will hammer the queue if autoq is off
				// Not sure if that's a problem?
				// TODO: investiage if it is
				time.Sleep(1 * time.Second)
			}

			// This lock is used to remotely pause here if necessary.
			// If the lock is unlocked, all that will happen is the program moving on,
			// otherwise we will wait until the lock is released elsewhere
			mixer.playLock.Lock()
			mixer.playLock.Unlock()
			mixer.Output <- broadcastPacket
		}
	}()

	return mixer
}

// Will swap the next song in place of the current one.
// TODO: Fails because fetchNextSong could be empty
func (m *Mixer) Skip() {
	m.currentSong = m.nextSong
	m.currentSongPath = m.nextSongPath
	m.nextSong, m.currentSongPath, _ = m.fetchNextSong()
}

// Will toggle playing by allowing writes to output
func (m *Mixer) Play() {
	m.playLock.Unlock()
}

// Will toggle playing by preventing writes to output
// TODO: FiX THIS. BORKED AS HELL https://github.com/VivaLaPanda/uta-stream/issues/3
// If people keep calling pause then it will keep spawning deadlocked routines
// until someone hits play, at which point all extra paused routines will die
// Need someway to check mutex or some different pause approach entirely
func (m *Mixer) Pause() {
	go func() {
		m.playLock.Lock()
	}()
}

// Will go to queue and get the next track
func (m *Mixer) fetchNextSong() (nextSongChan *chan []byte, nextSongPath string, isEmpty bool) {
	nextSongPath, isEmpty = m.queue.Pop()
	var nextSongReader io.Reader
	var err error
	if isEmpty {
		if m.cache.Hotstream != nil {
			// The queue is empty but we have a hotstream, which means something
			// is being converted urgently for us. Just start playing, ipfs/songdata
			// will show up as unknown
			nextSongReader = m.cache.Hotstream
		} else {
			// Empty and now hotstream, there really is nothing for us to do
			return nil, "", true
		}
	} else {
		// The queue isn't empty so we'll go get the provided song
		nextSongReader, err = m.cache.FetchIpfs(nextSongPath)
		if err != nil {
			log.Printf("Failed to fetch song (%v). Err: %v\n", nextSongPath, err)
			return nil, "", true
		}
	}

	// Start encoding for broadcast
	nextSongChan, err = encoder.EncodeMP3(nextSongReader, m.packetsPerSecond)
	if err != nil {
		log.Printf("Failed to encode song (%v). Err: %v\n", nextSongPath, err)
		return nil, "", true
	}

	return nextSongChan, nextSongPath, false
}
