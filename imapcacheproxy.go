



package imapcacheproxy


import (
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

// CachedMessage represents a cached email message.
type CachedMessage struct {
	Headers map[string][]string
	Body    string
}

// Cache represents the in-memory cache for email messages.
type Cache struct {
	mu      sync.RWMutex
	mailbox map[string]map[uint32]CachedMessage
}

func Serve(address) {
	if len(os.Args) < 4 {
		fmt.Println("Usage: imap-caching-proxy <listen-address> <remote-server> <remote-user> <remote-password>")
		os.Exit(1)
	}

	listenAddr := os.Args[1]
	remoteServer := os.Args[2]
	remoteUser := os.Args[3]
	remotePassword := os.Args[4]

	cache := &Cache{mailbox: make(map[string]map[uint32]CachedMessage)}

	s := server.New(func(conn server.Conn) server.Handler {
		return &handler{conn, remoteServer, remoteUser, remotePassword, cache}
	})

	s.Addr = listenAddr
	s.AllowInsecureAuth = true

	log.Printf("Starting IMAP caching proxy server on %s...\n", listenAddr)
	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

type handler struct {
	conn           server.Conn
	remoteServer   string
	remoteUser     string
	remotePassword string
	cache          *Cache
	client         *client.Client
}

func (h *handler) Login(_ *server.ConnInfo, cmd *imap.Login) (server.State, error) {
	if cmd.Username != h.remoteUser || cmd.Password != h.remotePassword {
		return nil, server.ErrInvalidCredentials
	}

	// Connect to the remote IMAP server
	c, err := client.DialTLS(h.remoteServer, nil)
	if err != nil {
		return nil, err
	}
	h.client = c

	return nil, nil
}

func (h *handler) Logout(_ *server.ConnInfo) error {
	// Logout from the remote IMAP server
	if h.client != nil {
		h.client.Logout()
	}

	return nil
}

func (h *handler) Select(_ *server.ConnInfo, cmd *imap.Select) (server.Mailbox, error) {
	return &mailbox{h.remoteServer, cmd.Mailbox, h.cache, h.client}, nil
}

type mailbox struct {
	remoteServer string
	name         string
	cache        *Cache
	client       *client.Client
}

func (m *mailbox) Fetch(_ *server.ConnInfo, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- server.Message) error {
	m.cache.mu.RLock()
	defer m.cache.mu.RUnlock()

	cachedMessages, ok := m.cache.mailbox[m.name]
	if !ok {
		return nil
	}

	for seq, cachedMessage := range cachedMessages {
		if seqset.Contains(seq) {
			msg := &server.LiteralMessage{
				SeqNum:  seq,
				Date:    cachedMessage.Headers["Date"][0],
				Size:    uint32(len(cachedMessage.Body)),
				Flags:   []string{"\\Seen"},
				Body:    []byte(cachedMessage.Body),
				Items:   items,
				UID:     seq,
				Mailbox: m,
			}
			ch <- msg
		}
	}

	return nil
}

func (m *mailbox) List(_ *server.ConnInfo, _ *imap.List) error {
	// List mailboxes from the remote server
	mboxes, err := m.client.List("", "*")
	if err != nil {
		return err
	}

	// Send the list to the client
	for _, mbox := range mboxes {
		m.MailboxInfo(mbox.Name)
	}

	return nil
}


func (m *mailbox) Fetch(_ *server.ConnInfo, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- server.Message) error {
	m.cache.mu.RLock()
	defer m.cache.mu.RUnlock()

	cachedMessages, ok := m.cache.mailbox[m.name]
	if !ok {
		return nil
	}

	for seq, cachedMessage := range cachedMessages {
		if seqset.Contains(seq) {
			msg := &server.LiteralMessage{
				SeqNum:  seq,
				Date:    cachedMessage.Headers["Date"][0],
				Size:    uint32(len(cachedMessage.Body)),
				Flags:   []string{"\\Seen"},
				Body:    []byte(cachedMessage.Body),
				Items:   items,
				UID:     seq,
				Mailbox: m,
			}
			ch <- msg
		}
	}

	// Fetch missing messages from the remote server
	seqnums := seqset.Set
	for _, seq := range seqnums {
		if _, exists := cachedMessages[seq]; !exists {
			// Fetch the message from the remote server
			err := m.fetchMessage(seq, items, ch)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (m *mailbox) fetchMessage(seqNum uint32, items []imap.FetchItem, ch chan<- server.Message) error {
	seqSet := new(imap.SeqSet)
	seqSet.Add(seqNum)

	// Fetch the message from the remote server
	messages, err := m.client.Fetch(seqSet, items...)
	if err != nil {
		return err
	}

	// Process and cache the fetched messages
	for _, msg := range messages {
		cachedMessage := CachedMessage{
			Headers: make(map[string][]string),
			Body:    string(msg.Body),
		}

		// Populate the headers map
		for _, header := range msg.BodyStructure.Fields {
			cachedMessage.Headers[header.Name] = header.Value
		}

		// Cache the message
		m.cache.mu.Lock()
		if _, ok := m.cache.mailbox[m.name]; !ok {
			m.cache.mailbox[m.name] = make(map[uint32]CachedMessage)
		}
		m.cache.mailbox[m.name][seqNum] = cachedMessage
		m.cache.mu.Unlock()

		// Send the fetched message to the client
		ch <- &server.LiteralMessage{
			SeqNum:  seqNum,
			Date:    cachedMessage.Headers["Date"][0],
			Size:    uint32(len(cachedMessage.Body)),
			Flags:   []string{"\\Seen"},
			Body:    []byte(cachedMessage.Body),
			Items:   items,
			UID:     seqNum,
			Mailbox: m,
		}
	}

	return nil
}
