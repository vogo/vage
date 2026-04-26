/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package webfetch

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type htmlExtraction struct {
	title     string
	markdown  string
	truncated bool
	warning   string
}

func extractHTML(body []byte, maxChars int) (htmlExtraction, error) {
	root, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return htmlExtraction{}, fmt.Errorf("web_fetch: failed to parse HTML")
	}

	title := strings.TrimSpace(extractTitle(root))
	main := findPreferredContentNode(root)

	scriptCount := countAtoms(root, atom.Script)
	noscriptText := strings.ToLower(strings.TrimSpace(nodeText(findFirst(root, atom.Noscript))))

	markdown := strings.TrimSpace(renderNodeMarkdown(main))
	if markdown == "" {
		markdown = strings.TrimSpace(renderNodeMarkdown(root))
	}

	if shouldTreatAsDynamic(markdown, scriptCount, noscriptText) {
		return htmlExtraction{title: title}, fmt.Errorf("web_fetch: page appears to require JavaScript rendering; use a browser-capable tool")
	}

	if title != "" && !strings.HasPrefix(markdown, "# ") {
		markdown = "# " + title + "\n\n" + markdown
	}

	markdown, truncated := truncateText(markdown, maxChars)
	return htmlExtraction{title: title, markdown: markdown, truncated: truncated}, nil
}

func renderNodeMarkdown(n *xhtml.Node) string {
	if n == nil {
		return ""
	}

	var blocks []string
	var walk func(*xhtml.Node)
	walk = func(cur *xhtml.Node) {
		if cur == nil {
			return
		}

		if cur.Type == xhtml.ElementNode {
			switch cur.DataAtom {
			case atom.Script, atom.Style, atom.Noscript, atom.Svg, atom.Nav, atom.Footer, atom.Header, atom.Aside, atom.Form:
				return
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				level := headingLevel(cur.DataAtom)
				text := strings.TrimSpace(collapseWhitespace(nodeText(cur)))
				if text != "" {
					blocks = append(blocks, strings.Repeat("#", level)+" "+text)
				}
				return
			case atom.Pre:
				text := strings.TrimSpace(nodeText(cur))
				if text != "" {
					blocks = append(blocks, "```\n"+text+"\n```")
				}
				return
			case atom.Code:
				if cur.Parent != nil && cur.Parent.DataAtom == atom.Pre {
					return
				}
			case atom.Ul, atom.Ol:
				items := renderList(cur)
				if items != "" {
					blocks = append(blocks, items)
				}
				return
			case atom.Table:
				table := renderTable(cur)
				if table != "" {
					blocks = append(blocks, table)
				}
				return
			case atom.P, atom.Article, atom.Section, atom.Main, atom.Div, atom.Blockquote:
				text := strings.TrimSpace(collapseWhitespace(renderInline(cur)))
				if text != "" {
					if cur.DataAtom == atom.Blockquote {
						text = "> " + strings.ReplaceAll(text, "\n", "\n> ")
					}
					blocks = append(blocks, text)
				}
			}
		}

		for child := cur.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(n)
	return dedupeBlocks(blocks)
}

func renderInline(n *xhtml.Node) string {
	if n == nil {
		return ""
	}

	if n.Type == xhtml.TextNode {
		return stdhtml.UnescapeString(n.Data)
	}

	if n.Type != xhtml.ElementNode && n.Type != xhtml.DocumentNode {
		return ""
	}

	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript:
		return ""
	case atom.Br:
		return "\n"
	case atom.Code:
		if n.Parent != nil && n.Parent.DataAtom == atom.Pre {
			return ""
		}
		text := strings.TrimSpace(collapseWhitespace(nodeText(n)))
		if text == "" {
			return ""
		}
		return "`" + text + "`"
	case atom.A:
		text := strings.TrimSpace(collapseWhitespace(nodeText(n)))
		href := attr(n, "href")
		if text == "" {
			return ""
		}
		if href == "" {
			return text
		}
		return "[" + text + "](" + href + ")"
	}

	var sb strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		sb.WriteString(renderInline(child))
	}
	return sb.String()
}

func renderList(n *xhtml.Node) string {
	var lines []string
	index := 1
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != xhtml.ElementNode || child.DataAtom != atom.Li {
			continue
		}
		text := strings.TrimSpace(collapseWhitespace(renderInline(child)))
		if text == "" {
			continue
		}
		prefix := "- "
		if n.DataAtom == atom.Ol {
			prefix = fmt.Sprintf("%d. ", index)
		}
		lines = append(lines, prefix+text)
		index++
	}
	return strings.Join(lines, "\n")
}

func renderTable(n *xhtml.Node) string {
	var rows [][]string
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		collectRows(child, &rows)
	}
	if len(rows) == 0 {
		return ""
	}

	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	if width == 0 {
		return ""
	}

	for i := range rows {
		for len(rows[i]) < width {
			rows[i] = append(rows[i], "")
		}
	}

	var out []string
	header := "|" + strings.Join(rows[0], "|") + "|"
	out = append(out, header)

	separators := make([]string, width)
	for i := range separators {
		separators[i] = "---"
	}
	out = append(out, "|"+strings.Join(separators, "|")+"|")
	for _, row := range rows[1:] {
		out = append(out, "|"+strings.Join(row, "|")+"|")
	}
	return strings.Join(out, "\n")
}

func collectRows(n *xhtml.Node, rows *[][]string) {
	if n == nil {
		return
	}
	if n.Type == xhtml.ElementNode && n.DataAtom == atom.Tr {
		var row []string
		for cell := n.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type != xhtml.ElementNode {
				continue
			}
			if cell.DataAtom != atom.Td && cell.DataAtom != atom.Th {
				continue
			}
			row = append(row, strings.TrimSpace(collapseWhitespace(renderInline(cell))))
		}
		if len(row) > 0 {
			*rows = append(*rows, row)
		}
		return
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		collectRows(child, rows)
	}
}

func shouldTreatAsDynamic(markdown string, scriptCount int, noscriptText string) bool {
	if strings.Contains(noscriptText, "enable javascript") || strings.Contains(noscriptText, "requires javascript") {
		return true
	}
	plain := strings.TrimSpace(markdown)
	return len(plain) < 160 && scriptCount >= 5
}

func extractTitle(root *xhtml.Node) string {
	title := findElement(root, atom.Title)
	if title == nil {
		return ""
	}
	return collapseWhitespace(nodeText(title))
}

func findPreferredContentNode(root *xhtml.Node) *xhtml.Node {
	for _, selector := range []struct {
		atom atom.Atom
		id   string
	}{
		{atom.Main, ""},
		{atom.Article, ""},
		{atom.Div, "content"},
		{atom.Div, "main-content"},
		{atom.Div, "docs-content"},
	} {
		if node := findByAtomOrID(root, selector.atom, selector.id); node != nil {
			return node
		}
	}
	return findElement(root, atom.Body)
}

func findByAtomOrID(root *xhtml.Node, tag atom.Atom, id string) *xhtml.Node {
	var walk func(*xhtml.Node) *xhtml.Node
	walk = func(n *xhtml.Node) *xhtml.Node {
		if n == nil {
			return nil
		}
		if n.Type == xhtml.ElementNode {
			if id != "" && strings.EqualFold(attr(n, "id"), id) {
				return n
			}
			if id == "" && n.DataAtom == tag {
				return n
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if found := walk(child); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(root)
}

func findElement(root *xhtml.Node, tag atom.Atom) *xhtml.Node {
	return findFirst(root, tag)
}

func findFirst(root *xhtml.Node, tag atom.Atom) *xhtml.Node {
	var walk func(*xhtml.Node) *xhtml.Node
	walk = func(n *xhtml.Node) *xhtml.Node {
		if n == nil {
			return nil
		}
		if n.Type == xhtml.ElementNode && n.DataAtom == tag {
			return n
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if found := walk(child); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(root)
}

func countAtoms(root *xhtml.Node, tag atom.Atom) int {
	count := 0
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n == nil {
			return
		}
		if n.Type == xhtml.ElementNode && n.DataAtom == tag {
			count++
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return count
}

func nodeText(n *xhtml.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == xhtml.TextNode {
		return stdhtml.UnescapeString(n.Data)
	}
	var sb strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		sb.WriteString(nodeText(child))
		if child.Type == xhtml.ElementNode && (child.DataAtom == atom.P || child.DataAtom == atom.Br || child.DataAtom == atom.Div || child.DataAtom == atom.Li) {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func headingLevel(a atom.Atom) int {
	switch a {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	case atom.H4:
		return 4
	case atom.H5:
		return 5
	default:
		return 6
	}
}
