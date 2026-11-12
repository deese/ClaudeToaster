//go:build windows

// Toasts through raw WinRT (no PowerShell, so Constrained Language Mode is
// irrelevant). Slots and IIDs were checked against the mingw-w64
// windows.ui.notifications.h / windows.data.xml.dom.h headers.
package main

import (
	"fmt"
	"html"
	"syscall"
	"unsafe"
)

const (
	// STA, like the UIA client: a thread cannot switch COM apartment models.
	roInitSingleThreaded = 0

	vtToastStaticsCreateNotifierWithID = 7
	vtToastNotifierShow                = 6
	vtToastFactoryCreate               = 6
	vtXMLDocumentIOLoadXML             = 6
)

var (
	iidToastStatics  = guid{0x50ac103f, 0xd235, 0x4598, [8]byte{0xbb, 0xef, 0x98, 0xfe, 0x4d, 0x1a, 0x3a, 0xd4}}
	iidToastFactory  = guid{0x04124b20, 0x82c6, 0x4229, [8]byte{0xb1, 0x09, 0xfd, 0x9e, 0xd4, 0x66, 0x2b, 0x53}}
	iidXMLDocumentIO = guid{0x6cd0e74e, 0xee65, 0x4489, [8]byte{0x9e, 0xbf, 0xca, 0x43, 0xe8, 0x7b, 0xa6, 0x37}}
	iidXMLDocument   = guid{0xf7f3a506, 0x1e87, 0x42d6, [8]byte{0xbc, 0xfb, 0xb8, 0xc8, 0x09, 0xfa, 0x54, 0x94}}
)

var (
	combase                 = syscall.NewLazyDLL("combase.dll")
	procRoInitialize        = combase.NewProc("RoInitialize")
	procRoGetActivationFact = combase.NewProc("RoGetActivationFactory")
	procRoActivateInstance  = combase.NewProc("RoActivateInstance")
	procWindowsCreateString = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString = combase.NewProc("WindowsDeleteString")
)

// showToast displays a toast. A non-empty launchURI makes a click launch that
// protocol URI.
func showToast(title, body, launchURI string) error {
	if r, _, _ := procRoInitialize.Call(roInitSingleThreaded); int32(r) < 0 {
		return fmt.Errorf("RoInitialize: 0x%08x", uint32(r))
	}

	statics, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastStatics)
	if err != nil {
		return err
	}
	defer statics.release()

	hID, freeID, err := newHString(appUserModelID)
	if err != nil {
		return err
	}
	defer freeID()
	var notifier unsafe.Pointer
	if err := statics.call(vtToastStaticsCreateNotifierWithID, hID, uintptr(unsafe.Pointer(&notifier))); err != nil {
		return fmt.Errorf("CreateToastNotifierWithId: %w", err)
	}
	defer comObj{notifier}.release()

	doc, err := newToastXML(title, body, launchURI)
	if err != nil {
		return err
	}
	defer doc.release()

	factory, err := activationFactory("Windows.UI.Notifications.ToastNotification", &iidToastFactory)
	if err != nil {
		return err
	}
	defer factory.release()

	var toast unsafe.Pointer
	if err := factory.call(vtToastFactoryCreate, uintptr(doc.p), uintptr(unsafe.Pointer(&toast))); err != nil {
		return fmt.Errorf("CreateToastNotification: %w", err)
	}
	defer comObj{toast}.release()

	return comObj{notifier}.call(vtToastNotifierShow, uintptr(toast))
}

// newToastXML builds an IXmlDocument for the toast.
func newToastXML(title, body, launchURI string) (comObj, error) {
	root := "<toast>"
	if launchURI != "" {
		root = fmt.Sprintf(`<toast activationType="protocol" launch="%s">`, html.EscapeString(launchURI))
	}
	xml := fmt.Sprintf(`%s<visual><binding template="ToastGeneric"><text>%s</text><text>%s</text></binding></visual></toast>`,
		root, html.EscapeString(title), html.EscapeString(body))

	hClass, freeClass, err := newHString("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return comObj{}, err
	}
	defer freeClass()
	var inspectable unsafe.Pointer
	if r, _, _ := procRoActivateInstance.Call(hClass, uintptr(unsafe.Pointer(&inspectable))); int32(r) < 0 {
		return comObj{}, fmt.Errorf("RoActivateInstance(XmlDocument): 0x%08x", uint32(r))
	}
	base := comObj{inspectable}
	defer base.release()

	var io unsafe.Pointer
	if err := base.call(vtQueryInterface, uintptr(unsafe.Pointer(&iidXMLDocumentIO)), uintptr(unsafe.Pointer(&io))); err != nil {
		return comObj{}, fmt.Errorf("QI IXmlDocumentIO: %w", err)
	}
	defer comObj{io}.release()

	hXML, freeXML, err := newHString(xml)
	if err != nil {
		return comObj{}, err
	}
	defer freeXML()
	if err := (comObj{io}).call(vtXMLDocumentIOLoadXML, hXML); err != nil {
		return comObj{}, fmt.Errorf("LoadXml: %w", err)
	}

	var doc unsafe.Pointer
	if err := base.call(vtQueryInterface, uintptr(unsafe.Pointer(&iidXMLDocument)), uintptr(unsafe.Pointer(&doc))); err != nil {
		return comObj{}, fmt.Errorf("QI IXmlDocument: %w", err)
	}
	return comObj{doc}, nil
}

func activationFactory(class string, iid *guid) (comObj, error) {
	h, free, err := newHString(class)
	if err != nil {
		return comObj{}, err
	}
	defer free()
	var out unsafe.Pointer
	r, _, _ := procRoGetActivationFact.Call(h, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if int32(r) < 0 {
		return comObj{}, fmt.Errorf("RoGetActivationFactory(%s): 0x%08x", class, uint32(r))
	}
	return comObj{out}, nil
}

// newHString creates a WinRT HSTRING and returns it with its release func.
func newHString(s string) (uintptr, func(), error) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, nil, err
	}
	var h uintptr
	// u includes the trailing NUL; the HSTRING length excludes it.
	if r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h))); int32(r) < 0 {
		return 0, nil, fmt.Errorf("WindowsCreateString: 0x%08x", uint32(r))
	}
	return h, func() { procWindowsDeleteString.Call(h) }, nil
}
