package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"

	"github.com/joho/godotenv"
	"github.com/julienschmidt/httprouter"
	sp "github.com/op/go-libspotify/spotify"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		println("Couldnt load configuration")
		return
	}
	session := newSession(os.Getenv("USERNAME"), os.Getenv("PASSWORD"), os.Getenv("APP_KEY"))
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

func newSession(username string, password string, appKeyPath string) *sp.Session {
	appKey, _ := ioutil.ReadFile(appKeyPath)
	audio, err := newAudioWriter()
	if err != nil {
		panic(err)
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
	if err = session.Login(credentials, false); err != nil {
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
