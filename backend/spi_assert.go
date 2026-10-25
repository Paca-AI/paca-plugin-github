package main

import gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"

// Compile-time proof that the provider implements the SPI and exactly the
// optional interfaces Describe declares (the conformance suite checks the
// declarations match).
var (
	_ gp.Provider          = (*ghProvider)(nil)
	_ gp.ThreadResolver    = (*ghProvider)(nil)
	_ gp.FileReader        = (*ghProvider)(nil)
	_ gp.LabelManager      = (*ghProvider)(nil)
	_ gp.ReviewerRequester = (*ghProvider)(nil)
	_ gp.AssigneeSetter    = (*ghProvider)(nil)
	_ gp.Merger            = (*ghProvider)(nil)
	_ gp.IdentityDeleter   = (*ghProvider)(nil)
	_ gp.AccessGranter     = (*ghProvider)(nil)
)
