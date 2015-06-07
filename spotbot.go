package main

import (
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"

	"code.google.com/p/portaudio-go/portaudio"

	"github.com/julienschmidt/httprouter"
	sp "github.com/op/go-libspotify/spotify"
)

var (
	appKeyPath = flag.String("key", "spotify_appkey.key", "path to app.key")
	username   = flag.String("u", "o.p", "spotify username")
	password   = flag.String("p", "", "spotify password")
	remember   = flag.Bool("remember", false, "remember username and password")
)

func main() {
	flag.Parse()
	session := spotify(*username, *password, *appKeyPath, *remember)
	router := httprouter.New()
	router.GET("/playlists", sessionRequest(session, listPlaylists))
	router.GET("/player/:action", sessionRequest(session, player))
	router.GET("/load/:trackId", sessionRequest(session, loadTrack))
	router.GET("/search/:name", sessionRequest(session, searchTrack))
	http.ListenAndServe(":3000", router)
}

func loadTrack(session *sp.Session, ps httprouter.Params) {
	linkStr := ps.ByName("trackId")
	println(linkStr)
	link, err := session.ParseLink(linkStr)
	if err != nil {
		println(err)
	}
	if track, err := link.Track(); err != nil {
		println("problems retrieving track")
		println(err)
	} else {
		track.Wait()
		player := session.Player()
		println(track.Name())
		if err = player.Load(track); err != nil {
			println("problems loading track")
			println(err)
		}
	}
}

func player(session *sp.Session, ps httprouter.Params) {
	actionStr := ps.ByName("action")
	player := session.Player()
	switch actionStr {
	case "play":
		player.Play()
	case "pause":
		player.Pause()
	}
}

func searchTrack(session *sp.Session, ps httprouter.Params) {
	searchString := ps.ByName("name")
	spec := sp.SearchSpec{0, 10}
	opts := sp.SearchOptions{Tracks: spec}
	search, err := session.Search(searchString, &opts)
	if err != nil {
		panic(err)
	}
	search.Wait()
	for i := 0; i < search.Tracks(); i++ {
		track := search.Track(i)
		println(track.Name(), track.Link().String())
	}
}

func listPlaylists(session *sp.Session, ps httprouter.Params) {
	container, err := session.Playlists()
	if err != nil {
		println(err)
	}
	container.Wait()
	for i := 0; i < container.Playlists(); i++ {
		if container.PlaylistType(i) == sp.PlaylistTypePlaylist {
			println(container.Playlist(i).Name())
		}
	}
}

func sessionRequest(session *sp.Session, fn func(session *sp.Session, ps httprouter.Params)) func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		if session != nil {
			fn(session, ps)
		}
	}
}

func spotify(username string, password string, appKeyPath string, remember bool) *sp.Session {
	appKey, _ := ioutil.ReadFile(appKeyPath)
	audio, err := newAudioWriter()
	if err != nil {
		panic("NOOOOOOO")
		//panic(err)
	}
	session, err := sp.NewSession(&sp.Config{
		ApplicationKey:   appKey,
		ApplicationName:  "prog",
		CacheLocation:    "tmp",
		SettingsLocation: "tmp",
		AudioConsumer:    audio,
	})

	exit := make(chan bool)

	go loginLoop(session, audio, exit)
	go signalLoop(exit)
	credentials := sp.Credentials{
		Username: username,
		Password: password,
	}
	if err = session.Login(credentials, remember); err != nil {
		log.Fatal(err)
	}
	return session
}

func signalLoop(exit chan<- bool) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, os.Kill)
	for _ = range signals {
		select {
		case exit <- true:
		default:
		}
	}
}

func loginLoop(session *sp.Session, audio *audioWriter, exit <-chan bool) {
	running := true
	for running {
		select {
		case <-session.LoggedInUpdates():
			println("logged in")
		case msg := <-session.MessagesToUser():
			println(msg)
		case logMsg := <-session.LogMessages():
			println(logMsg.Message)
		case <-session.LoggedOutUpdates():
			running = false
			break
		case <-exit:
			user, _ := session.CurrentUser()
			println(user.CanonicalName())
			session.Logout()
			audio.Close()
		}
	}
	session.Close()
}

var (
	// audioInputBufferSize is the number of delivered data from libspotify before
	// we start rejecting it to deliver any more.
	audioInputBufferSize = 8

	// audioOutputBufferSize is the maximum number of bytes to buffer before
	// passing it to PortAudio.
	audioOutputBufferSize = 8192
)

// audio wraps the delivered Spotify data into a single struct.
type audio struct {
	format sp.AudioFormat
	frames []byte
}

// audioWriter takes audio from libspotify and outputs it through PortAudio.
type audioWriter struct {
	input chan audio
	quit  chan bool
	wg    sync.WaitGroup
}

// newAudioWriter creates a new audioWriter handler.
func newAudioWriter() (*audioWriter, error) {
	w := &audioWriter{
		input: make(chan audio, audioInputBufferSize),
		quit:  make(chan bool, 1),
	}

	stream, err := newPortAudioStream()
	if err != nil {
		return w, err
	}

	w.wg.Add(1)
	go w.streamWriter(stream)
	return w, nil
}

// Close stops and closes the audio stream and terminates PortAudio.
func (w *audioWriter) Close() error {
	select {
	case w.quit <- true:
	default:
	}
	w.wg.Wait()
	return nil
}

// WriteAudio implements the spotify.AudioWriter interface.
func (w *audioWriter) WriteAudio(format sp.AudioFormat, frames []byte) int {
	select {
	case w.input <- audio{format, frames}:
		return len(frames)
	default:
		return 0
	}
}

// streamWriter reads data from the input buffer and writes it to the output
// portaudio buffer.
func (w *audioWriter) streamWriter(stream *portAudioStream) {
	defer w.wg.Done()
	defer stream.Close()

	buffer := make([]int16, audioOutputBufferSize)
	output := buffer[:]

	for {
		// Wait for input data or signal to quit.
		var input audio
		select {
		case input = <-w.input:
		case <-w.quit:
			return
		}

		// Initialize the audio stream based on the specification of the input format.
		err := stream.Stream(&output, input.format.Channels, input.format.SampleRate)
		if err != nil {
			panic(err)
		}

		// Decode the incoming data which is expected to be 2 channels and
		// delivered as int16 in []byte, hence we need to convert it.
		i := 0
		for i < len(input.frames) {
			j := 0
			for j < len(buffer) && i < len(input.frames) {
				buffer[j] = int16(input.frames[i]) | int16(input.frames[i+1])<<8
				j += 1
				i += 2
			}

			output = buffer[:j]
			stream.Write()
		}
	}
}

// portAudioStream manages the output stream through PortAudio when requirement
// for number of channels or sample rate changes.
type portAudioStream struct {
	device *portaudio.DeviceInfo
	stream *portaudio.Stream

	channels   int
	sampleRate int
}

// newPortAudioStream creates a new portAudioStream using the default output
// device found on the system. It will also take care of automatically
// initialise the PortAudio API.
func newPortAudioStream() (*portAudioStream, error) {
	if err := portaudio.Initialize(); err != nil {
		return nil, err
	}
	out, err := portaudio.DefaultHostApi()
	if err != nil {
		portaudio.Terminate()
		return nil, err
	}
	return &portAudioStream{device: out.DefaultOutputDevice}, nil
}

// Close closes any open audio stream and terminates the PortAudio API.
func (s *portAudioStream) Close() error {
	if err := s.reset(); err != nil {
		portaudio.Terminate()
		return err
	}
	return portaudio.Terminate()
}

func (s *portAudioStream) reset() error {
	if s.stream != nil {
		if err := s.stream.Stop(); err != nil {
			return err
		}
		if err := s.stream.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Stream prepares the stream to go through the specified buffer, channels and
// sample rate, re-using any previously defined stream or setting up a new one.
func (s *portAudioStream) Stream(buffer *[]int16, channels int, sampleRate int) error {
	if s.stream == nil || s.channels != channels || s.sampleRate != sampleRate {
		if err := s.reset(); err != nil {
			return err
		}

		params := portaudio.HighLatencyParameters(nil, s.device)
		params.Output.Channels = channels
		params.SampleRate = float64(sampleRate)
		params.FramesPerBuffer = len(*buffer)

		stream, err := portaudio.OpenStream(params, buffer)
		if err != nil {
			return err
		}
		if err := stream.Start(); err != nil {
			stream.Close()
			return err
		}

		s.stream = stream
		s.channels = channels
		s.sampleRate = sampleRate
	}
	return nil
}

// Write pushes the data in the buffer through to PortAudio.
func (s *portAudioStream) Write() error {
	return s.stream.Write()
}
