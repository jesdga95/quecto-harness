Search the web; each result carries the part of its page that answers the query, so a result is read, not fetched.

query: what to search for, a sentence or a set of terms.
results: how many results to return. One budget of text is shared across the results, so fewer results carry more of each page.
full: the page from the top instead of the parts that answer the query, for when which part matters is not known in advance. Keep results at 1 or 2 with it. full is the JSON literal true or false, lowercase and unquoted.
since: only pages published on or after this date, YYYY-MM-DD. For anything that changes: a price, a version, what is current.

A result arriving cut short means the budget ran out on it: answer with a narrower query rather than asking for the rest.
