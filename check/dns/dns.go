package dns

import (
	"context"
	"net"
	"strings"

	"github.com/foxcpp/maddy/address"
	"github.com/foxcpp/maddy/check"
	"github.com/foxcpp/maddy/exterrors"
	"github.com/foxcpp/maddy/module"
)

func requireMatchingRDNS(ctx check.StatelessCheckContext) module.CheckResult {
	if ctx.MsgMeta.SrcRDNSName == nil {
		ctx.Logger.Msg("rDNS lookup is disabled, skipping")
		return module.CheckResult{}
	}

	rdnsName, ok := ctx.MsgMeta.SrcRDNSName.Get().(string)
	if !ok {
		// There is no way to tell temporary failure from permanent one here
		// so err on the side of caution.
		return module.CheckResult{
			Reason: &exterrors.SMTPError{
				Code:         40,
				EnhancedCode: exterrors.EnhancedCode{4, 7, 25},
				Message:      "DNS lookup failure during policy check",
				CheckName:    "require_matching_rdns",
			},
		}
	}

	srcDomain := strings.TrimSuffix(ctx.MsgMeta.SrcHostname, ".")
	rdnsName = strings.TrimSuffix(rdnsName, ".")

	if strings.EqualFold(rdnsName, srcDomain) {
		ctx.Logger.Debugf("PTR record %s matches source domain, OK", rdnsName)
		return module.CheckResult{}
	}

	return module.CheckResult{
		Reason: &exterrors.SMTPError{
			Code:         550,
			EnhancedCode: exterrors.EnhancedCode{5, 7, 25},
			Message:      "rDNS name does not match source hostname",
			CheckName:    "require_matching_rdns",
		},
	}
}

func requireMXRecord(ctx check.StatelessCheckContext, mailFrom string) module.CheckResult {
	if mailFrom == "" {
		// Permit null reverse-path for bounces.
		return module.CheckResult{}
	}

	_, domain, err := address.Split(mailFrom)
	if err != nil {
		return module.CheckResult{
			Reason: exterrors.WithFields(err, map[string]interface{}{
				"check": "require_matching_rdns",
			}),
		}
	}
	if domain == "" {
		return module.CheckResult{
			Reason: &exterrors.SMTPError{
				Code:         501,
				EnhancedCode: exterrors.EnhancedCode{5, 1, 8},
				Message:      "No domain part",
				CheckName:    "require_mx_record",
			},
		}
	}

	_, ok := ctx.MsgMeta.SrcAddr.(*net.TCPAddr)
	if !ok {
		ctx.Logger.Println("non-TCP/IP source")
		return module.CheckResult{}
	}

	srcMx, err := ctx.Resolver.LookupMX(context.Background(), domain)
	if err != nil {
		code := 501
		enchCode := exterrors.EnhancedCode{5, 7, 27}
		if exterrors.IsTemporary(err) {
			code = 420
			enchCode = exterrors.EnhancedCode{4, 7, 27}
		}

		return module.CheckResult{
			Reason: &exterrors.SMTPError{
				Code:         code,
				EnhancedCode: enchCode,
				Message:      "DNS lookup failure during policy check",
				CheckName:    "require_mx_record",
				Err:          err,
				// Since net and miekg/dns errors are not annotated with
				// context information, Err: err is essentially no-op. Provide
				// original error text for logging explicitly.
				Misc: map[string]interface{}{
					"reason": err.Error(),
				},
			},
		}
	}

	if len(srcMx) == 0 {
		return module.CheckResult{
			Reason: &exterrors.SMTPError{
				Code:         501,
				EnhancedCode: exterrors.EnhancedCode{5, 7, 27},
				Message:      "Domain in MAIL FROM has no MX records",
				CheckName:    "require_mx_record",
			},
		}
	}

	return module.CheckResult{}
}

func requireMatchingEHLO(ctx check.StatelessCheckContext) module.CheckResult {
	tcpAddr, ok := ctx.MsgMeta.SrcAddr.(*net.TCPAddr)
	if !ok {
		ctx.Logger.Printf("non-TCP/IP source, skipped")
		return module.CheckResult{}
	}

	ehlo := ctx.MsgMeta.SrcHostname

	if strings.HasPrefix(ehlo, "[") && strings.HasSuffix(ehlo, "]") {
		// IP in EHLO, checking against source IP directly.

		ehloIP := net.ParseIP(ehlo[1 : len(ehlo)-1])
		if ehloIP == nil {
			return module.CheckResult{
				Reason: &exterrors.SMTPError{
					Code:         550,
					EnhancedCode: exterrors.EnhancedCode{5, 7, 0},
					Message:      "Malformed IP in EHLO",
					CheckName:    "require_matching_ehlo",
				},
			}
		}

		if !ehloIP.Equal(tcpAddr.IP) {
			return module.CheckResult{
				Reason: &exterrors.SMTPError{
					Code:         550,
					EnhancedCode: exterrors.EnhancedCode{5, 7, 0},
					Message:      "IP in EHLO is not the same as actual client IP",
					CheckName:    "require_matching_ehlo",
				},
			}
		}

		return module.CheckResult{}
	}

	srcIPs, err := ctx.Resolver.LookupIPAddr(context.Background(), ehlo)
	if err != nil {
		code := 501
		enchCode := exterrors.EnhancedCode{5, 7, 27}
		if exterrors.IsTemporary(err) {
			code = 420
			enchCode = exterrors.EnhancedCode{4, 7, 27}
		}

		return module.CheckResult{
			Reason: &exterrors.SMTPError{
				Code:         code,
				EnhancedCode: enchCode,
				Message:      "DNS lookup failure during policy check",
				CheckName:    "require_matching_ehlo",
				Err:          err,
				// Since net and miekg/dns errors are not annotated with
				// context information, Err: err is essentially no-op. Provide
				// original error text for logging explicitly.
				Misc: map[string]interface{}{
					"reason": err.Error(),
				},
			},
		}
	}

	for _, ip := range srcIPs {
		if tcpAddr.IP.Equal(ip.IP) {
			ctx.Logger.Debugf("A/AAA record found for %s for %s domain", tcpAddr.IP, ehlo)
			return module.CheckResult{}
		}
	}
	return module.CheckResult{
		Reason: &exterrors.SMTPError{
			Code:         550,
			EnhancedCode: exterrors.EnhancedCode{5, 7, 0},
			Message:      "No matching A/AAA records found for EHLO hostname",
			CheckName:    "require_matching_ehlo",
		},
	}
}

func init() {
	check.RegisterStatelessCheck("require_matching_rdns", check.FailAction{Quarantine: true},
		requireMatchingRDNS, nil, nil, nil)
	check.RegisterStatelessCheck("require_mx_record", check.FailAction{Quarantine: true},
		nil, requireMXRecord, nil, nil)
	check.RegisterStatelessCheck("require_matching_ehlo", check.FailAction{Quarantine: true},
		requireMatchingEHLO, nil, nil, nil)
}
