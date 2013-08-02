package main

import (
  "code.google.com/p/go.net/websocket"
  "container/list"
  "sync/atomic"
  "net/http"
  "runtime"
  "strings"
  "bufio"
  "time"
  "net"
  "fmt"
)

var userIdCounter uint64
var messageIdCounter uint64

var allowedRanges = []string{
  "85.188.16.0/20",
  "85.188.32.0/19",
}

func RoomInfoUpdater( chatRoom *ChatRoom ) {
  for {
    runtime.GC()
    fmt.Printf( "Goroutines: %d\n", runtime.NumGoroutine() )
    time.Sleep( 1 * time.Second )
  }
}

func MessageRemover( messageBuffer *[]*Message ) {
  for {
    if len( *messageBuffer ) > 50 {
      *messageBuffer = (*messageBuffer)[len( *messageBuffer )-49:]
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
        client.writer.WriteString(data)
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

func NewClient(connection net.Conn) *Client {
  userClass := 0

  if IPAllowed( strings.Split(connection.RemoteAddr().String(), ":")[0] ) {
    userClass = 1
  } else {
    connection.Close()
    return nil
  }

  writer := bufio.NewWriter(connection)
  reader := bufio.NewReader(connection)

  client := &Client{
    id: atomic.AddUint64( &userIdCounter, 1 ),
    ipString: connection.RemoteAddr().String(),
    incoming: make(chan string),
    outgoing: make(chan string),
    quit: false,
    reader: reader,
    writer: writer,
    userClass: userClass,
  }

  client.Listen()
  return client
}

func ClientCloser( client *Client, chatRoom *ChatRoom ) {
  for !client.quit {
    time.Sleep( 100 * time.Millisecond )
  }
  chatRoom.RemoveClient( client )
}

type Message struct {
  id uint64
  senderId uint64
  postTime time.Time
  acceptionTime time.Time
  message string
}

func NewMessage( client *Client, msg string ) *Message {
  message := &Message{
    id: atomic.AddUint64( &messageIdCounter, 1 ),
    senderId: client.id,
    acceptionTime: time.Unix( 0, 0 ),
    message: msg,
  }
  return message
}

type ChatRoom struct {
  clients  *list.List
  messages []*Message
  joins chan net.Conn
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

func (chatRoom *ChatRoom) Join(connection net.Conn) {
  client := NewClient(connection)

  if client == nil {
    return
  }

  go ClientCloser( client, chatRoom )

  go func() {
    for _, msg := range chatRoom.messages {
      client.outgoing <- msg.message
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

  fmt.Println( "Client joined." )
}

func (chatRoom *ChatRoom) HandleMessage( msg *Message ) {
  chatRoom.incoming <- msg.message
  chatRoom.messages = append( chatRoom.messages, msg )
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
    messages: make([]*Message, 0),
    joins: make(chan net.Conn),
    incoming: make(chan string),
    outgoing: make(chan string),
  }

  go MessageRemover( &chatRoom.messages )
  go RoomInfoUpdater( chatRoom )

  chatRoom.Listen()
  return chatRoom
}

func main() {
  chatRoom := NewChatRoom()
  listener, _ := net.Listen("tcp", ":6666")

  userIdCounter = 0
  messageIdCounter = 0

  fmt.Println( "Waiting for clients." )

  for {
    conn, _ := listener.Accept()
    chatRoom.joins <- conn
  }
}

