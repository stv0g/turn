package server

import (
	"crypto/md5"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"gitlab.com/pions/pion/pkg/go/stun"
)

const (
	maximumLifetime = 3600 // https://tools.ietf.org/html/rfc5766#section-6.2 defines 3600 recommendation
	defaultLifetime = 600  // https://tools.ietf.org/html/rfc5766#section-2.2 defines 600 recommendation
)

type TurnServer struct {
	stunServer  *StunServer
	relayServer *RelayServer
}

// Is there really no stdlib for this?
func min(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

//TODO, include time info support stale nonces
func buildNonce() string {
	h := md5.New()
	now := time.Now().Unix()
	_, _ = io.WriteString(h, strconv.FormatInt(now, 10))
	_, _ = io.WriteString(h, strconv.FormatInt(rand.Int63(), 10))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// https://tools.ietf.org/html/rfc5766#section-6.2
func (s *TurnServer) handleAllocateRequest(addr *net.UDPAddr, m *stun.Message) error {

	// https://tools.ietf.org/html/rfc5389#section-10.2.2
	validateAuthentication := func() (ok bool, err error) {
		_, integrityFound := m.GetAttribute(stun.AttrMessageIntegrity)
		if integrityFound == false {
			err = buildAndSend(s.stunServer.connection, addr, stun.ClassErrorResponse, stun.MethodAllocate, m.TransactionID,
				&stun.Err401Unauthorized,
				&stun.Nonce{buildNonce()},
				&stun.Realm{"pion.sh"}, //TODO use env variable, check for FQDN on startup
			)
		} else {
			usernameRawAttr, usernameFound := m.GetAttribute(stun.AttrUsername)
			realmRawAttr, realmFound := m.GetAttribute(stun.AttrRealm)
			nonceRawAttr, nonceFound := m.GetAttribute(stun.AttrNonce)
			if !usernameFound {
				err = errors.Errorf("Integrity found, but missing username")
			}
			if !realmFound {
				err = errors.Errorf("Integrity found, but missing realm")
			}
			if !nonceFound {
				err = errors.Errorf("Integrity found, but missing nonce")
			}
			if err != nil {
				if sendErr := buildAndSend(s.stunServer.connection, addr, stun.ClassErrorResponse, stun.MethodAllocate, m.TransactionID,
					&stun.Err400BadRequest,
				); sendErr != nil {
					err = errors.Errorf(strings.Join([]string{sendErr.Error(), err.Error()}, "\n"))
				}

				return
			}
			usernameAttr := &stun.Username{}
			realmAttr := &stun.Realm{}
			nonceAttr := &stun.Nonce{}

			err = usernameAttr.Unpack(m, usernameRawAttr)
			if err != nil {
				return
			}
			err = realmAttr.Unpack(m, realmRawAttr)
			if err != nil {
				return
			}
			err = nonceAttr.Unpack(m, nonceRawAttr)
			if err == nil {
				ok = true
			}
		}
		return
	}

	if ok, err := validateAuthentication(); ok == false || err != nil {
		return err
	}

	// 2. The server checks if the 5-tuple is currently in use by an
	//    existing allocation.  If yes, the server rejects the request with
	//    a 437 (Allocation Mismatch) error.
	// 5-TUPLE source IP address/port number, destination
	// IP address/port number and the protocol in use

	// There shouldn't be one relay server, but a collection of them? TODO
	if s.relayServer.isAllocated(addr) {
		//stun.AttrErrorCode, 437
	}

	// 3. The server checks if the request contains a REQUESTED-TRANSPORT
	//    attribute.  If the REQUESTED-TRANSPORT attribute is not included
	//    or is malformed, the server rejects the request with a 400 (Bad
	//    Request) error.  Otherwise, if the attribute is included but
	//    specifies a protocol other that UDP, the server rejects the
	//    request with a 442 (Unsupported Transport Protocol) error.
	if _, ok := m.GetAttribute(stun.AttrRequestedTransport); ok {
		//stun.AttrErrorCode, 442
	}
	// rt := RequestedTransport{}
	// rt.Unpack(m,...)
	// rt validation

	// 4. The request may contain a DONT-FRAGMENT attribute.  If it does,
	//    but the server does not support sending UDP datagrams with the DF
	//    bit set to 1 (see Section 12), then the server treats the DONT-
	//    FRAGMENT attribute in the Allocate request as an unknown
	//    comprehension-required attribute.
	if _, ok := m.GetAttribute(stun.AttrDontFragment); ok {
		// Should we handle manually building UDP packets to be able to set this bit?
	}

	// 5.  The server checks if the request contains a RESERVATION-TOKEN
	//  attribute.  If yes, and the request also contains an EVEN-PORT
	// attribute, then the server rejects the request with a 400 (Bad
	// Request) error.  Otherwise, it checks to see if the token is
	// valid (i.e., the token is in range and has not expired and the
	// corresponding relayed transport address is still available).  If
	// the token is not valid for some reason, the server rejects the
	// request with a 508 (Insufficient Capacity) error.
	if _, ok := m.GetAttribute(stun.AttrReservationToken); ok {
		if _, ok := m.GetAttribute(stun.AttrEvenPort); ok {
			//stun.AttrErrorCode, 400
		}

		// Unpack reservation token
		// if re not valid { stun.AttrErrorCode, 508 }
	}

	// 6. The server checks if the request contains an EVEN-PORT attribute.
	//    If yes, then the server checks that it can satisfy the request
	//    (i.e., can allocate a relayed transport address as described
	//    below).  If the server cannot satisfy the request, then the
	//    server rejects the request with a 508 (Insufficient Capacity)
	//    error.
	if _, ok := m.GetAttribute(stun.AttrEvenPort); ok {
		// validate
		// if unable to allocate { stun.AttrErrorCode, 508 }
	}

	// 7. At any point, the server MAY choose to reject the request with a
	//    486 (Allocation Quota Reached) error if it feels the client is
	//    trying to exceed some locally defined allocation quota.  The
	//    server is free to define this allocation quota any way it wishes,
	//    but SHOULD define it based on the username used to authenticate
	//    the request, and not on the client's transport address.
	// Check redis for username allocs of transports
	// if err return { stun.AttrErrorCode, 486 }

	//8. Also at any point, the server MAY choose to reject the request
	//   with a 300 (Try Alternate) error if it wishes to redirect the
	//   client to a different server.  The use of this error code and
	//   attribute follow the specification in [RFC5389].
	// Check current usage vs redis usage of other servers
	// if bad, redirect { stun.AttrErrorCode, 300 }

	//var lifetimeDuration uint32 = defaultLifetime
	//r = m.GetAttribute(stun.AttrLifetime)
	//if r != nil {
	//	lt := turn.Lifetime{}
	//	if err := lt.Unpack(m, r); err != nil {
	//		return errors.Wrap(err, "invalid lifetime")
	//	}
	//
	//	lifetimeDuration = min(lt.Duration, maximumLifetime)
	//}
	//
	//rsp := stun.Message{}
	//r, err := turn.Lifetime{Duration: lifetimeDuration}.Pack(m)
	//if err != nil {
	//	return errors.Wrap(err, "unable to pack lifetime")
	//}

	return nil
}

func (s *TurnServer) handleRefreshRequest(addr *net.UDPAddr, m *stun.Message) error {
	return nil
}

func (s *TurnServer) handleCreatePermissionRequest(addr *net.UDPAddr, m *stun.Message) error {
	return nil
}

func (s *TurnServer) handleSendIndication(addr *net.UDPAddr, m *stun.Message) error {
	return nil
}

func (s *TurnServer) handleChannelBindRequest(addr *net.UDPAddr, m *stun.Message) error {
	return nil
}

func (s *TurnServer) Listen(address string, port int) error {
	return s.stunServer.Listen(address, port)
}

func NewTurnServer() *TurnServer {
	t := TurnServer{}
	t.stunServer = NewStunServer()

	t.stunServer.handlers[HandlerKey{stun.ClassRequest, stun.MethodAllocate}] = func(addr *net.UDPAddr, m *stun.Message) error {
		return t.handleAllocateRequest(addr, m)
	}
	t.stunServer.handlers[HandlerKey{stun.ClassRequest, stun.MethodRefresh}] = func(addr *net.UDPAddr, m *stun.Message) error {
		return t.handleRefreshRequest(addr, m)
	}
	t.stunServer.handlers[HandlerKey{stun.ClassRequest, stun.MethodCreatePermission}] = func(addr *net.UDPAddr, m *stun.Message) error {
		return t.handleCreatePermissionRequest(addr, m)
	}
	t.stunServer.handlers[HandlerKey{stun.ClassIndication, stun.MethodSend}] = func(addr *net.UDPAddr, m *stun.Message) error {
		return t.handleSendIndication(addr, m)
	}
	t.stunServer.handlers[HandlerKey{stun.ClassRequest, stun.MethodChannelBind}] = func(addr *net.UDPAddr, m *stun.Message) error {
		return t.handleChannelBindRequest(addr, m)
	}

	return &t
}
