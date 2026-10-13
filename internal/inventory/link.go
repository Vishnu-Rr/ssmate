package inventory

import "strings"

// Link folds records of machines registered from elsewhere into the records
// of the machines themselves.
//
// The same server can be listed twice: once by the cloud it runs in, and once
// by another cloud's session service that its agent registered with. Shown
// as two rows, the better way in sits on a row that looks like a different
// machine. Linked, the machine's own record gains that way in and the other
// row disappears into it.
//
// A link is made on the first of these that answers, strongest first:
//
//   - what the registering service recorded about the source machine;
//   - the ssmate:source tag, set by whoever registered it;
//   - the address and the hostname together, when each pair is unique on
//     both sides.
//
// An address alone never links two records, because private addresses repeat
// across networks: 10.0.0.4 is in use in thousands of them. A record that
// cannot be linked is left as its own row, since a wrong merge would send a
// session to the wrong machine, and a duplicate row is the lesser harm.
//
// Link never changes the records it is given, so it can run on every load,
// cached or fresh: the cache keeps each inventory's own records, and the
// joined view is rebuilt from them each time.
func Link(nodes []Node) []Node {
	out := make([]Node, len(nodes))
	for i := range nodes {
		out[i] = nodes[i].clone()
	}

	byKey := map[string][]int{}
	byAddress := map[string][]int{}
	for i, n := range out {
		if n.Outside != nil {
			continue
		}
		for _, k := range append([]string{n.ID}, n.Aliases...) {
			if k != "" {
				byKey[strings.ToLower(k)] = append(byKey[strings.ToLower(k)], i)
			}
		}
		if a := addressKey(n); a != "" {
			byAddress[a] = append(byAddress[a], i)
		}
	}
	outsideAt := map[string]int{}
	for _, n := range out {
		if n.Outside != nil {
			if a := addressKey(n); a != "" {
				outsideAt[a]++
			}
		}
	}

	folded := make([]bool, len(out))
	for i, n := range out {
		if n.Outside == nil {
			continue
		}
		home, how := -1, ""
		for _, try := range []struct{ key, how string }{
			{n.Outside.SourceID, LinkSource},
			{n.Outside.Tag, LinkTag},
		} {
			if try.key == "" || strings.EqualFold(try.key, n.ID) {
				continue
			}
			if hits := byKey[strings.ToLower(try.key)]; len(hits) == 1 {
				home, how = hits[0], try.how
				break
			}
		}
		if home < 0 {
			if a := addressKey(n); a != "" && outsideAt[a] == 1 && len(byAddress[a]) == 1 {
				home, how = byAddress[a][0], LinkAddress
			}
		}
		if home < 0 {
			continue
		}
		for _, r := range n.Routes {
			r.Link = how
			out[home].AddRoute(r)
		}
		out[home].Aliases = appendNew(out[home].Aliases, n.ID)
		folded[i] = true
	}

	kept := out[:0]
	for i, n := range out {
		if !folded[i] {
			kept = append(kept, n)
		}
	}
	return kept
}

// addressKey pairs a private address with the first label of a hostname, in
// lower case. Either missing means no key, so nothing links on half a match.
func addressKey(n Node) string {
	host := strings.ToLower(n.Hostname)
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	if n.PrivateIP == "" || host == "" {
		return ""
	}
	return n.PrivateIP + "|" + host
}

// clone copies a node deeply enough that changing the copy's routes or
// aliases never reaches the original.
func (n Node) clone() Node {
	c := n
	c.Routes = append([]Route(nil), n.Routes...)
	c.Aliases = append([]string(nil), n.Aliases...)
	return c
}

func appendNew(list []string, v string) []string {
	for _, have := range list {
		if strings.EqualFold(have, v) {
			return list
		}
	}
	return append(list, v)
}
