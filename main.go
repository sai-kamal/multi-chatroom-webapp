package main

import (
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"personal_website/models"
	"strconv"
	"time"

	"github.com/gorilla/schema"
	"github.com/gorilla/websocket"
	uuid "github.com/nu7hatch/gouuid"

	"github.com/gorilla/mux"
	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
)

//Page contains info to be sent to templates
type Page struct {
	Title   string
	User    models.User
	IsAuth  bool
	IsAdmin bool
	Route   string
	Data    interface{} //pass any extra data
}

// Declares all the variables that are to be initialized before running the server
var (
	Templates  *template.Template                  // contains all the templates
	Store      *sessions.CookieStore               //cookie store
	LiveUsers  map[string]models.User              // has info about all the users logged in
	LobbyUsers map[string]models.UserMeetingParams //contains userinfo who are in the process of joining a meeting
	//Upgrader upgrades the http connection
	Upgrader websocket.Upgrader
	IP       string
)

//Init helps in initializing different variables and running functions
func Init() {
	IP = findMyIP()
	models.DBinit()
	models.MeetingsInit()
	Templates = template.Must(template.ParseGlob("./html/*.gohtml"))
	LiveUsers = make(map[string]models.User) //stores all the users that have been logged in
	//to keep track of users who are going to enter a meeting
	LobbyUsers = make(map[string]models.UserMeetingParams)

	//related to session
	authKeyOne := securecookie.GenerateRandomKey(64)
	encryptionKeyOne := securecookie.GenerateRandomKey(32)
	Store = sessions.NewCookieStore(authKeyOne, encryptionKeyOne)
	Store.Options = &sessions.Options{
		// MaxAge:   60 * 15, //15 mins max for a cookie
		HttpOnly: true,
	}
	//related to sockets
	Upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
}

func findMyIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		os.Stderr.WriteString("Oops: " + err.Error() + "\n")
		os.Exit(1)
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}

//AuthRequired redirects the user to "/" page if not logged in
func AuthRequired(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username, sessionID := GetSessionDetails(r)
		if username == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if LiveUsers[username.(string)].SessionID != sessionID {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		handler.ServeHTTP(w, r)
	}
}

//RootHandler takes care of the "/" route
func RootHandler(w http.ResponseWriter, r *http.Request) {
	var user models.User
	username, id := GetSessionDetails(r)
	if username != nil {
		fmt.Println("username in cookie in ROOT", username)
		user = models.GetUser(username.(string), "/")
		//TODO: check the below if condition
		if user.Username == "" {
			log.Println("not a valid username and user in RootHandler", user)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if user.SessionID == id {
			http.Redirect(w, r, "/home", http.StatusSeeOther)
			return
		}
	} else {
		log.Println("no username in request for rootHandler")
	}
	data := Page{Title: "$K", User: user, IsAuth: false, Route: "/"}
	tErr := Templates.ExecuteTemplate(w, "index", data)
	if tErr != nil {
		log.Println("failed to execute '/' template", tErr)
	}
	return
}

//LogInPostHandler logins a user
func LogInPostHandler(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm() //getting form data
	if err != nil {
		log.Println("failed to parse form during log in")
	}
	var userForm models.User
	decoder := schema.NewDecoder()
	err = decoder.Decode(&userForm, r.PostForm)
	if err != nil {
		log.Println("failed to parse form from client", err)
	}
	//getting user info from DB
	userDB := models.GetUser(userForm.Username, "/login")
	if userDB.Username == "" {
		log.Println("failed to get user from DB log in")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	password := userDB.Password
	if password != userForm.Password {
		log.Println("passwords dont match", password, userForm.Password)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	//creating new sessiontoken
	sessionToken, uuidErr := uuid.NewV4()
	if uuidErr != nil {
		log.Println("failed to generate a uuid", uuidErr)
	}
	sessionTokenString := sessionToken.String()
	user := models.GetUser(userForm.Username, "/login")
	if user.Username == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	//writing the new session string to the DB
	measurement := "people"
	t, _ := time.Parse(time.RFC3339Nano, user.UTIME)
	tags := map[string]string{
		"username": user.Username,
		"password": user.Password,
	}
	fields := map[string]interface{}{
		"name":       user.Name,
		"session_id": sessionTokenString,
		"admin":      user.IsAdmin,
	}
	models.DBwrite(measurement, tags, fields, t)
	//creating neww session
	session, sErr := Store.Get(r, "session")
	if sErr != nil {
		log.Println("failed to get a session in LogInHandler", sErr)

	}
	session.Values["username"] = user.Username
	session.Values["session_id"] = sessionTokenString
	saveErr := session.Save(r, w)
	if saveErr != nil {
		log.Println("session saving error", saveErr)
	}
	user.SessionID = sessionTokenString
	LiveUsers[user.Username] = user
	http.Redirect(w, r, "/home", http.StatusSeeOther)
	return
}

//GetSessionDetails gets the username and session id from the cookie
func GetSessionDetails(r *http.Request) (username, sessionID interface{}) {
	session, sErr := Store.Get(r, "session")
	if sErr != nil {
		log.Println("failed to get a session in GetSessionDetails", sErr)
	}
	username, _ = session.Values["username"]
	sessionID, _ = session.Values["session_id"]
	return
}

//HomeHandler executes the template after the user logins "/home"
func HomeHandler(w http.ResponseWriter, r *http.Request) {
	username, _ := GetSessionDetails(r)
	user := LiveUsers[username.(string)]
	data := Page{Title: "Home Page", User: user, IsAuth: true, IsAdmin: user.IsAdmin}
	tErr := Templates.ExecuteTemplate(w, "home", data)
	if tErr != nil {
		log.Println("failed to execute '/home' template", tErr)
	}
	return
}

//TestHandler is used to test random pages and routes
func TestHandler(w http.ResponseWriter, r *http.Request) {
	tErr := Templates.ExecuteTemplate(w, "room", nil)
	if tErr != nil {
		log.Println("failed to execute '/meetings' template", tErr)
	}
}

//LogOutGetHandler logs out the user
func LogOutGetHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := Store.Get(r, "session")
	username, _ := session.Values["username"]
	delete(LiveUsers, username.(string))
	session.Values["username"] = nil
	session.Values["session_id"] = nil
	session.Options.MaxAge = -1 //very important
	sErr := session.Save(r, w)
	if sErr != nil {
		log.Println("failed to update session during logout", sErr)
	}
	log.Println(username, "user successfully logged out")
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return
}

//MeetingsHandler lists out all the meetings after querying the database
func MeetingsHandler(w http.ResponseWriter, r *http.Request) {
	meetingsDB := models.GetMeetings("/meetings") //get info of meetings from database
	username, _ := GetSessionDetails(r)
	user := LiveUsers[username.(string)]
	data := Page{Title: "Meetings", IsAuth: true, Data: meetingsDB, IsAdmin: user.IsAdmin}
	tErr := Templates.ExecuteTemplate(w, "meetings", data)
	if tErr != nil {
		log.Println("failed to execute '/meetings' template", tErr)
	}
	return
}

//JoinMeetingHandler joins the user to a meeting and redirects him to the chatroom page
func JoinMeetingHandler(w http.ResponseWriter, r *http.Request) {
	username, _ := GetSessionDetails(r)
	err := r.ParseForm()
	if err != nil {
		log.Println("failed to parse form in joinMeeting", err)
	}
	//creating new user for websocket and meeting
	var userInMeetingForm models.UserMeetingParams
	decoder := schema.NewDecoder() //receives delay, importance of meeting and meeting_name
	err = decoder.Decode(&userInMeetingForm, r.PostForm)
	if err != nil {
		log.Println("failed to parse form in joinMeeting", err)
	}
	userInMeetingForm.Username = username.(string)
	LobbyUsers[username.(string)] = userInMeetingForm
	http.Redirect(w, r, "/chatroom", http.StatusSeeOther)
	return
}

//ChatroomHandler executes the chatroom template. This is a reduntant function just to change the url.
//So no form has to be resubmitted multiple times
func ChatroomHandler(w http.ResponseWriter, r *http.Request) {
	username, _ := GetSessionDetails(r)
	user := LiveUsers[username.(string)]
	userInMeeting := LobbyUsers[username.(string)]
	server := models.ChatServers[userInMeeting.MeetingName]
	dataStr := struct {
		OrigExpect int64
		TimeSpace  int64
		TimeDiff   int64
	}{
		OrigExpect: server.MeetingParams.OrigExpect,
		TimeSpace:  server.MeetingParams.TimeSpace,
		TimeDiff:   server.MeetingParams.TimeDiff,
	}
	data := Page{Title: "ChatRoom", IsAuth: true, IsAdmin: user.IsAdmin, Data: dataStr}
	tErr := Templates.ExecuteTemplate(w, "chatroom", data)
	if tErr != nil {
		log.Println("failed to execute '/chatroom' template", tErr)
	}
	return
}

// SeeLogHandler redirects the user to the chatroom Page
func SeeLogHandler(w http.ResponseWriter, r *http.Request) {
	return
}

//ChatHandler takes care of the chatroom websocket
func ChatHandler(w http.ResponseWriter, r *http.Request) {
	username, _ := GetSessionDetails(r)
	userInMeeting := LobbyUsers[username.(string)]
	delete(LobbyUsers, username.(string))
	//upgrade the connection to websocket
	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("failed to upgrade connection", err)
	}
	server := models.ChatServers[userInMeeting.MeetingName]
	models.CreateUserMeetingParams(conn, server, &userInMeeting)
	server.AddUser(&userInMeeting)
	userInMeeting.Listen()
	fmt.Println("\n\nchat handler function ended for ", username)
	http.Redirect(w, r, "/feedback", http.StatusSeeOther) //after meeting for user, redirected to feedback page
	defer conn.Close()
}

// FeedBackGetHandler displays the feedback page
func FeedBackGetHandler(w http.ResponseWriter, r *http.Request) {
	username, _ := GetSessionDetails(r)
	user := LiveUsers[username.(string)]
	data := Page{Title: "FeedBack", User: user, IsAuth: true, IsAdmin: user.IsAdmin}
	tErr := Templates.ExecuteTemplate(w, "feedback", data)
	if tErr != nil {
		log.Println("failed to execute '/feedback' template", tErr)
	}
}

// FeedBackPostHandler records user's experience of the agent's action in meeting
func FeedBackPostHandler(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		log.Println("failed to parse form during create meeting")
	}
	var meeting models.Meeting
	decoder := schema.NewDecoder()
	err = decoder.Decode(&meeting, r.PostForm)
	if err != nil {
		log.Println("failed to parse form from client in createMeeting", err)
	}
	username, _ := GetSessionDetails(r)
	measurement := strconv.Itoa(int(meeting.Name))
	tags := map[string]string{}
	fields := map[string]interface{}{
		"feedback": float64(meeting.Feedback),
		"user":     username.(string),
	}
	t := time.Now()
	models.DBwrite(measurement, tags, fields, t)
	http.Redirect(w, r, "/meetings", http.StatusSeeOther)
	return
}

func main() {
	Init()

	r := mux.NewRouter()
	r.HandleFunc("/", RootHandler).Methods("GET")
	r.HandleFunc("/login", LogInPostHandler).Methods("POST")
	r.HandleFunc("/logout", AuthRequired(LogOutGetHandler)).Methods("GET")
	r.HandleFunc("/home", AuthRequired(HomeHandler)).Methods("GET")
	r.HandleFunc("/meetings", AuthRequired(MeetingsHandler)).Methods("GET")
	r.HandleFunc("/createMeeting", AuthRequired(models.CreateMeetingHandler)).Methods("POST")
	r.HandleFunc("/joinMeeting", AuthRequired(JoinMeetingHandler)).Methods("POST")
	r.HandleFunc("/seeLog{id}", AuthRequired(SeeLogHandler)).Methods("GET")
	r.HandleFunc("/chat", AuthRequired(ChatHandler)).Methods("GET")
	r.HandleFunc("/chatroom", AuthRequired(ChatroomHandler)).Methods("GET")
	r.HandleFunc("/startMeeting{meetingName}", AuthRequired(models.StartMeetingHandler)).Methods("GET")
	r.HandleFunc("/feedback", AuthRequired(FeedBackGetHandler)).Methods("GET")
	r.HandleFunc("/feedback", AuthRequired(FeedBackPostHandler)).Methods("POST")
	r.HandleFunc("/test", TestHandler).Methods("GET")

	r.PathPrefix("/public/").Handler(http.FileServer(http.Dir(".")))

	fmt.Println("running server on port 9090")
	http.ListenAndServe(":9090", r)
}
