// SPDX-License-Identifier: MIT

package transport

import (
	"strconv"
	"strings"
	"sync"

	"go_gateway/protobuf"
)

// Order IDs are namespaced per authenticated trader before they reach the
// core ("<trader>::<client id>"), so traders cannot collide with or address
// each other's orders. The namespace is stripped again on the way back.
const ownerSep = "::"

// orderRoute is the orderClientMap value: where to deliver reports for an
// order and which trader namespace to strip from them.
// provisional marks a route registered by a cancel/replace request for an
// order the gateway had no route for; it is dropped if the core rejects the
// request and promoted on any other report.
type orderRoute struct {
	conn        any
	owner       string
	provisional bool
}

// pendingReplace maps an original order ID to the provisional replacement ID
// registered for an in-flight cancel/replace, so a rejected replace can drop it.
var pendingReplace sync.Map

// registerRoute records where reports for orderID go. New orders always
// (re)register; provisional routes never overwrite an existing route, so a
// cancel/replace request cannot redirect a live order's reports.
// Returns whether the route was stored.
func registerRoute(orderID string, r orderRoute) bool {
	if !r.provisional {
		orderClientMap.Store(orderID, r)
		return true
	}
	_, loaded := orderClientMap.LoadOrStore(orderID, r)
	return !loaded
}

// inflightRequests maps an order ID to the connection of a cancel/replace
// request whose target order is routed to a different connection (e.g. a
// second session of the same trader), so the reply reaches the requester
// while fills keep going to the original session.
var inflightRequests sync.Map

// registerRequest registers the routes for a cancel/replace request.
// Returns whether a new (provisional) order route was stored.
func registerRequest(orderID string, conn any, owner string) bool {
	if registerRoute(orderID, orderRoute{conn: conn, owner: owner, provisional: true}) {
		return true
	}
	if v, ok := orderClientMap.Load(orderID); ok && v.(orderRoute).conn != conn {
		inflightRequests.Store(orderID, conn)
	}
	return false
}

// dropProvisional removes orderID's route if it is still provisional.
func dropProvisional(orderID string) {
	if v, ok := orderClientMap.Load(orderID); ok && v.(orderRoute).provisional {
		orderClientMap.Delete(orderID)
	}
}

func namespacedOrderID(owner, id string) string {
	if id == "" {
		return ""
	}
	return owner + ownerSep + id
}

// bindOwner stamps the authenticated trader on order-entry messages,
// overriding any client-supplied sender, and namespaces their order IDs.
func bindOwner(msg *protobuf.FXPMessage, traderID string) {
	switch p := msg.Payload.(type) {
	case *protobuf.FXPMessage_NewOrder:
		p.NewOrder.Sender = traderID
		p.NewOrder.OrderId = namespacedOrderID(traderID, p.NewOrder.OrderId)
		// STP groups are scoped to the trader so a client cannot name another
		// trader's group (and, e.g., cancel its resting orders with cancel-oldest).
		p.NewOrder.StpGroup = stpGroupID(traderID, p.NewOrder.StpGroup)
	case *protobuf.FXPMessage_OrderCancel:
		p.OrderCancel.Sender = traderID
		p.OrderCancel.OrderId = namespacedOrderID(traderID, p.OrderCancel.OrderId)
	case *protobuf.FXPMessage_OrderCancelReplace:
		p.OrderCancelReplace.Sender = traderID
		p.OrderCancelReplace.OrderId = namespacedOrderID(traderID, p.OrderCancelReplace.OrderId)
		p.OrderCancelReplace.NewOrderId = namespacedOrderID(traderID, p.OrderCancelReplace.NewOrderId)
	case *protobuf.FXPMessage_OrderMassCancel:
		p.OrderMassCancel.Sender = traderID
	}
}

// orderOwner returns the (already bound) sender of an order-entry message.
func orderOwner(msg *protobuf.FXPMessage) string {
	switch p := msg.Payload.(type) {
	case *protobuf.FXPMessage_NewOrder:
		return p.NewOrder.Sender
	case *protobuf.FXPMessage_OrderCancel:
		return p.OrderCancel.Sender
	case *protobuf.FXPMessage_OrderCancelReplace:
		return p.OrderCancelReplace.Sender
	}
	return ""
}

// stripOwner restores client-visible order IDs on an execution report.
func stripOwner(rep *protobuf.ExecutionReport, owner string) {
	if owner == "" {
		return
	}
	prefix := owner + ownerSep
	rep.OrderId = strings.TrimPrefix(rep.OrderId, prefix)
	rep.OrigOrderId = strings.TrimPrefix(rep.OrigOrderId, prefix)
	rep.ExecutionId = strings.Replace(rep.ExecutionId, prefix, "", 1)
}

// stpGroupID scopes a client STP group to its trader. The length prefix keeps
// it unambiguous when trader IDs themselves contain ownerSep.
func stpGroupID(owner, group string) string {
	if group == "" {
		return ""
	}
	return strconv.Itoa(len(owner)) + ":" + owner + ownerSep + group
}
