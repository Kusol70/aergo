/**
 * @file    preprocess.c
 * @copyright defined in aergo/LICENSE.txt
 */

#include "common.h"

#include "util.h"
#include "strbuf.h"
#include "stack.h"

#include "prep.h"

static void substitue(char *path, stack_t *imp, strbuf_t *out);

static void
scan_init(scan_t *scan, char *path, strbuf_t *out)
{
    scan->path = path;
    scan->fp = open_file(path, "r");

    yypos_init(&scan->loc);

    scan->buf_len = 0;
    scan->buf_pos = 0;
    scan->buf[0] = '\0';

    scan->out = out;
}

static char
scan_next(scan_t *scan)
{
    char c;

    if (scan->buf_pos >= scan->buf_len) {
        scan->buf_len = fread(scan->buf, 1, sizeof(scan->buf), scan->fp);
        if (scan->buf_len == 0)
            return EOF;

        scan->buf_pos = 0;
    }

    c = scan->buf[scan->buf_pos++];
    
    if (c == '\n' || c == '\r')
        scan->loc.line++;

    scan->loc.offset++;

    return c;
}

static char
scan_peek(scan_t *scan, int cnt)
{
    if (scan->buf_pos + cnt >= scan->buf_len) {
        scan->buf_len -= scan->buf_pos;
        memmove(scan->buf, scan->buf + scan->buf_pos, scan->buf_len);
        scan->buf_pos = 0;

        scan->buf_len +=
            fread(scan->buf + scan->buf_len, 1, 
                  sizeof(scan->buf) - scan->buf_len, scan->fp);
        if (scan->buf_len <= cnt)
            return EOF;
    }

    return scan->buf[scan->buf_pos + cnt];
}

static void
add_file(char *path, stack_t *imp)
{
    stack_node_t *node = stack_top(imp);

    if (node == NULL) {
        stack_push(imp, xstrdup(path));
        return;
    }

    while (true) {
        if (strcmp(node->item, path) == 0)
            FATAL(ERROR_CROSS_IMPORT, path);
    }

    stack_push(imp, xstrdup(path));
}

static void
put_char(scan_t *scan, char c)
{
    strbuf_append(scan->out, &c, 1);
}

static void
put_comment(scan_t *scan, char c)
{
    char n;

    put_char(scan, c);

    if (scan_peek(scan, 0) == '*') {
        while ((n = scan_next(scan)) != EOF) {
            put_char(scan, n);

            if (n == '*' && scan_peek(scan, 0) == '/') {
                put_char(scan, scan_next(scan));
                break;
            }
        }
    }
    else if (scan_peek(scan, 0) == '/') {
        while ((n = scan_next(scan)) != EOF) {
            put_char(scan, n);

            if (n == '\n' || n == '\r')
                break;
        }
    }
}

static void
put_literal(scan_t *scan, char c)
{
    char n;

    put_char(scan, c);

    while ((n = scan_next(scan)) != EOF) {
        put_char(scan, n);

        if (n != '\\' && scan_peek(scan, 0) == '"') {
            put_char(scan, scan_next(scan));
            break;
        }
    }
}

/* need to keep just "void" for tests */
void
mark_file(char *path, int line, int offset, strbuf_t *out)
{
    char buf[PATH_MAX_LEN + 16];

    snprintf(buf, sizeof(buf), "#file \"%s\" %d %d\n", path, line, offset);

    strbuf_append(out, buf, strlen(buf));
}

static void
put_import(scan_t *scan, stack_t *imp)
{
    int offset = 0;
    char path[PATH_MAX_LEN];
    char c, n;

    while ((c = scan_next(scan)) != EOF) {
        if (c == '"') {
            while ((n = scan_next(scan)) != EOF) {
                path[offset++] = n;

                if (n != '\\' && scan_peek(scan, 0) == '"') {
                    path[offset] = '\0';

                    mark_file(path, 1, 0, scan->out);
                    substitue(path, imp, scan->out);
                    mark_file(path, scan->loc.line + 1, scan->loc.offset, 
                              scan->out);

                    stack_pop(imp);
                    offset = 0;
                    break;
                }
            }
        }
        else if (c == '\n' || c == '\r') {
            break;
        }
    }
}

static void
substitue(char *path, stack_t *imp, strbuf_t *out)
{
    bool is_first_ch = true;
    char c;
    scan_t scan;

    scan_init(&scan, path, out);

    add_file(path, imp);

    while ((c = scan_next(&scan)) != EOF) {
        if (c == '/') {
            put_comment(&scan, c);
            is_first_ch = false;
        }
        else if (c == '"') {
            put_literal(&scan, c);
            is_first_ch = false;
        }
        else if (c == '\n' || c == '\r') {
            put_char(&scan, c);
            is_first_ch = true;
        }
        else if (c == ' ' || c == '\t' || c == '\f') {
            put_char(&scan, c);
        }
        else if (is_first_ch && c == 'i' &&
                 scan_peek(&scan, 0) == 'm' &&
                 scan_peek(&scan, 0) == 'p' &&
                 scan_peek(&scan, 0) == 'o' &&
                 scan_peek(&scan, 0) == 'r' &&
                 scan_peek(&scan, 0) == 't' &&
                 isblank(scan_peek(&scan, 0))) {
            put_import(&scan, imp);
            is_first_ch = false;
        }
        else {
            put_char(&scan, c);
            is_first_ch = false;
        }
    }
}

void
preprocess(char *path, strbuf_t *out)
{
    stack_t imp;

    stack_init(&imp);

    substitue(path, &imp, out);
}

/* end of preprocess.c */
