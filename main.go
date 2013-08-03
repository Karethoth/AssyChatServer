package main

import (
  "code.google.com/p/go.net/websocket"
  "container/list"
  "encoding/json"
  "sync/atomic"
  "net/http"
  "runtime"
  "strings"
  "strconv"
  "bytes"
  "bufio"
  "time"
  "html"
  "net"
  "fmt"
)

var userIdCounter uint64
var messageIdCounter uint64

var allowedRanges = []string{
  "85.188.16.0/20",
  "85.188.32.0/19",
}

var messageTimeLog map[string]time.Time
func MessageTimeOK( msg *Message ) bool {
  ip := strings.Split( msg.sender.ipString, ":" )[0]

  if time.Since( messageTimeLog[ip] ) < 60 * time.Second {
    return false
  }
  messageTimeLog[ip] = time.Now()
  return true
}

var activeConnections list.List
func ConnectionAlive( conn *websocket.Conn ) bool {
  for it := activeConnections.Front(); it != nil; it = it.Next() {
    if bytes.Equal( []byte(it.Value.(string)), []byte(conn.Request().RemoteAddr) ) {
      return true
    }
  }
  return false
}

func RoomInfoUpdater( chatRoom *ChatRoom ) {
  for {
    runtime.GC()
    fmt.Printf( "================\n" )
    fmt.Printf( "Goroutines: %d\n", runtime.NumGoroutine() )
    fmt.Printf( "Users:      %d\n", chatRoom.clients.Len() )
    fmt.Printf( "Messages:   %d\n", chatRoom.messages.Len() )
    time.Sleep( 1 * time.Second )
  }
}

func MessageRemover( messageBuffer *list.List ) {
  for {
    for {
      if messageBuffer.Len() <= 50 {
        break
      }
      messageBuffer.Remove( messageBuffer.Front() )
    }
    time.Sleep( 200 * time.Millisecond )
  }
}

func IPAllowed(ip string) bool {
  for _, it := range allowedRanges {
    _, cidrnet, err := net.ParseCIDR(it)
    if err != nil {
      panic(err) // assuming I did it right above
    }
    parsedIP := net.ParseIP(ip)
    if cidrnet.Contains(parsedIP) {
      return true
    }
  }
  return false
}

type Client struct {
  id uint64
  ipString string
  userClass int
  incoming chan string
  outgoing chan string
  quit bool
  reader *bufio.Reader
  writer *bufio.Writer
  connection *websocket.Conn
  msgSent time.Time
}

func (client *Client) Read() {
  for {
    line, err := client.reader.ReadString('\n')
    if err != nil {
      client.quit = true
      return
    }

    select {
      case client.incoming <- line:
        continue
      case <-time.After( 1 * time.Second ):
        break
    }
  }
}

func (client *Client) Write() {
  for {
    if client.quit {
      return
    }
    select {
      case data := <-client.outgoing:
        client.writer.Write( []byte(data) )
        client.writer.Flush()
      case <-time.After( 1 * time.Second ):
        break
    }
  }
}

func (client *Client) Listen() {
  go client.Read()
  go client.Write()
}

func NewClient(connection websocket.Conn) *Client {
  userClass := 0

  if IPAllowed( strings.Split(connection.Request().RemoteAddr, ":")[0] ) {
    userClass = 1
  } else {
  }

  writer := bufio.NewWriter(&connection)
  reader := bufio.NewReader(&connection)

  client := &Client{
    id: atomic.AddUint64( &userIdCounter, 1 ),
    ipString: connection.Request().RemoteAddr,
    incoming: make(chan string),
    outgoing: make(chan string),
    quit: false,
    reader: reader,
    writer: writer,
    userClass: userClass,
    connection: &connection,
  }

  client.Listen()
  return client
}

func ClientCloser( client *Client, chatRoom *ChatRoom ) {
  for !client.quit {
    time.Sleep( 100 * time.Millisecond )
  }
  chatRoom.RemoveClient( client )

  for it := activeConnections.Front(); it != nil; it = it.Next() {
    if bytes.Equal( []byte(it.Value.(string)), []byte(client.ipString) ) {
      activeConnections.Remove( it )
    }
  }
}

type Message struct {
  Id uint64
  sender *Client
  PostTime int64
  AcceptionTime int64
  Message string
  originalMessage string
  length int
}

func NewMessage( client *Client, msg string ) *Message {
  message := &Message{
    Id: 0,
    sender: client,
    AcceptionTime: 0,
    Message: html.EscapeString( msg ),
    length: len(msg),
  }
  return message
}

type ChatRoom struct {
  clients  *list.List
  messages *list.List
  joins chan websocket.Conn
  incoming chan string
  outgoing chan string
}

func (chatRoom *ChatRoom) RemoveClient( client *Client ) {
  fmt.Println( "Removing client." )
  for it := chatRoom.clients.Front(); it != nil; it = it.Next() {
    if it.Value.(*Client) == client {
      if it.Value.(*Client).id == client.id {
        chatRoom.clients.Remove( it )
        fmt.Println( "Client removed." )
      }
    }
  }
}

func (chatRoom *ChatRoom) Broadcast(data string) {
  for it := chatRoom.clients.Front(); it != nil; it = it.Next() {
    if it.Value.(*Client).quit {
      continue
    }

    select {
      case it.Value.(*Client).outgoing <- data:
        continue
      case <-time.After( 10 * time.Second ):
        continue
    }
  }
}

func (chatRoom *ChatRoom) Join(connection websocket.Conn) {
  client := NewClient(connection)

  if client == nil {
    return
  }
  fmt.Println( "Client connecting" )

  go ClientCloser( client, chatRoom )

  go func() {
    for it := chatRoom.messages.Front(); it != nil; it = it.Next() {
      msg := it.Value.(*Message)
      if msg.AcceptionTime != 0 {
        client.outgoing <- msg.Message
      }
    }
  }()

  chatRoom.clients.PushBack(client)

  go func() {
    for {
      if client.quit {
        return
      }

      select {
        case msg := <-client.incoming:
          newMessage := NewMessage( client, msg )
          chatRoom.HandleMessage( newMessage )
        case <-time.After( 1 * time.Second ):
          if client.quit {
            return
          }
      }
    }
  }()

  fmt.Println( "Client connected succesfully." )
}

func (chatRoom *ChatRoom) HandleMessage( msg *Message ) {
  ignoreTimeOK := false
  if msg.sender.userClass == 0 {
    return
  }
  if msg.length > 144 || msg.length <= 5 {
    return
  }
  if !strings.HasPrefix( msg.Message, "MSG:" ) && msg.sender.userClass != 2 {
    if !strings.HasPrefix( msg.Message, "login:" ) {
      return
    }
    if strings.Split( msg.Message, ":" )[1] != "passu\n" {
      return
    }
    msg.sender.userClass = 2

    for it := chatRoom.messages.Front(); it != nil; it = it.Next() {
      msg := it.Value.(*Message)
      if msg.AcceptionTime == 0 {
        msg.sender.outgoing <- msg.Message
      }
    }
    return

  } else if strings.HasPrefix( msg.Message, "accept:" ) {
    idString := strings.Split( msg.Message, ":" )[1]
    idString = strings.Split( idString, "\n" )[0]
    id, _ := strconv.Atoi( idString )
    for it := chatRoom.messages.Front(); it != nil; it = it.Next() {
      itMsg := it.Value.(*Message)
      if itMsg.Id == uint64(id) {
        if itMsg.AcceptionTime != 0 {
          return
        }
        msg = NewMessage( msg.sender, itMsg.originalMessage )
        msg.AcceptionTime = time.Now().Unix()
        msg.PostTime = itMsg.PostTime
        msg.Id = itMsg.Id
        msg.sender = itMsg.sender
        ignoreTimeOK = true
        tmpJson, _ := json.Marshal( msg )
        msg.Message = string(tmpJson)
        chatRoom.messages.Remove( it )
        chatRoom.messages.PushBack( msg )
        chatRoom.incoming <- msg.Message
        return
      }
    }
    return
  } else if strings.HasPrefix( msg.Message, "forget:" ) {
    idString := strings.Split( msg.Message, ":" )[1]
    idString = strings.Split( idString, "\n" )[0]
    fmt.Println( idString )
    return
  } else if !strings.HasPrefix( msg.Message, "MSG:" ) {
    return
  }
  if msg.sender.userClass == 2 {
    ignoreTimeOK = true
  }
  if !ignoreTimeOK && !MessageTimeOK( msg ) {
    return
  }
  msg.Id = atomic.AddUint64( &messageIdCounter, 1 )
  msg.PostTime = time.Now().Unix()
  if msg.sender.userClass == 2 {
    msg.AcceptionTime = time.Now().Unix()
  }

  msg.originalMessage = msg.Message
  tmpJson, _ := json.Marshal( msg )
  msg.Message = string(tmpJson)

  msg.sender.msgSent = time.Now()
  chatRoom.messages.PushBack( msg )
  if msg.AcceptionTime != 0 {
    chatRoom.incoming <- msg.Message
  } else {
    for it := chatRoom.clients.Front(); it != nil; it = it.Next() {
      if it.Value.(*Client).userClass != 2 {
        continue
      }
      select {
        case it.Value.(*Client).outgoing <- msg.Message:
          continue
        case <-time.After( 10 * time.Second ):
          continue
      }
    }
  }
}

func (chatRoom *ChatRoom) Listen() {
  go func() {
    for {
      select {
        case data := <-chatRoom.incoming:
                   chatRoom.Broadcast(data)
        case conn := <-chatRoom.joins:
                   chatRoom.Join(conn)
      }
    }
  }()
}

func NewChatRoom() *ChatRoom {
  chatRoom := &ChatRoom{
    clients: list.New(),
    messages: list.New(),
    joins: make(chan websocket.Conn),
    incoming: make(chan string),
    outgoing: make(chan string),
  }

  go MessageRemover( chatRoom.messages )
  go RoomInfoUpdater( chatRoom )

  chatRoom.Listen()
  return chatRoom
}

var mainRoom *ChatRoom

func ChatServer( ws *websocket.Conn ) {
  mainRoom.joins <- *ws
  activeConnections.PushBack( ws.Request().RemoteAddr )
  for( ConnectionAlive( ws ) ) {
    time.Sleep( 1 * time.Second )
  }
}

func main() {
  messageTimeLog = make(map[string]time.Time)
  mainRoom = NewChatRoom()

  http.Handle( "/", websocket.Handler(ChatServer) )
  //listener, _ := net.Listen("tcp", ":1337")

  userIdCounter = 0
  messageIdCounter = 0

  fmt.Println( "Waiting for clients." )
  http.ListenAndServe( ":13337", nil )
}

