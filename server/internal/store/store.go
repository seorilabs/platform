// Package store는 Firestore 접근을 독점한다.
//
// 이 패키지 밖에서 firestore.Client를 만들지 않는다.
// Doc과 Collection이 string이 아니라 fspath.Path만 받고 fs 필드를
// export하지 않으므로, 환경 prefix 적용을 우회할 방법이 구조적으로 없다.
//
// staging은 production과 같은 (default) 데이터베이스를 공유한다.
// Firestore 무료 할당량이 (default)에만 적용되기 때문이다. ADR 0003 참고.
// 그래서 prefix 누락은 staging이 production 데이터를 건드리는 사고가 된다.
// 규율이 아니라 타입으로 막는다.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/seorilabs/platform/server/internal/fspath"
)

// ErrNotFound는 문서가 없을 때다.
//
// 호출자가 errors.Is로 판정한다. 없는 게 정상인 경우와 오류인 경우를
// 도메인이 판단해야 하므로 여기서 platformerr로 바꾸지 않는다.
var ErrNotFound = errors.New("store: 문서가 없다")

// ErrAborted는 트랜잭션 충돌이다. Firestore가 자동 재시도하지만
// 재시도 한도를 넘기면 이 에러로 나온다.
var ErrAborted = errors.New("store: 트랜잭션이 중단됐다")

// Client는 Firestore 접근점이다.
type Client struct {
	// fs는 export하지 않는다. 이게 경로 통제의 핵심이다.
	fs     *firestore.Client
	prefix string
}

// New는 클라이언트를 만든다.
//
// prefix가 빈 문자열이면 production이다. staging은 "stg_"를 쓴다.
// prefix에 슬래시가 들어가면 경로 구조가 깨지므로 거부한다.
func New(ctx context.Context, projectID, prefix string) (*Client, error) {
	if projectID == "" {
		return nil, errors.New("store: 프로젝트 ID가 필요하다")
	}
	if strings.Contains(prefix, "/") {
		return nil, fmt.Errorf("store: prefix에 슬래시를 넣을 수 없다: %q", prefix)
	}

	fs, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: Firestore 클라이언트 생성 실패: %w", err)
	}
	return &Client{fs: fs, prefix: prefix}, nil
}

// NewWithClient는 이미 만든 클라이언트를 감싼다. 테스트용이다.
func NewWithClient(fs *firestore.Client, prefix string) *Client {
	return &Client{fs: fs, prefix: prefix}
}

func (c *Client) Close() error { return c.fs.Close() }

// Prefix는 현재 환경 prefix를 돌려준다. 로깅과 진단용이다.
func (c *Client) Prefix() string { return c.prefix }

// resolve는 경로 검증과 prefix 적용을 한 곳에 모은다.
// 모든 접근이 이 함수를 통과한다.
func (c *Client) resolve(p fspath.Path, want fspath.Kind) (string, error) {
	if p.Kind() != want {
		return "", fmt.Errorf("store: %s 경로가 필요한데 %s다: %s", want, p.Kind(), p)
	}
	prefixed, err := p.WithEnvPrefix(c.prefix)
	if err != nil {
		return "", fmt.Errorf("store: prefix 적용 실패: %w", err)
	}
	return prefixed.String(), nil
}

// Doc은 문서 참조를 돌려준다.
func (c *Client) Doc(p fspath.Path) (*firestore.DocumentRef, error) {
	full, err := c.resolve(p, fspath.KindDocument)
	if err != nil {
		return nil, err
	}
	return c.fs.Doc(full), nil
}

// Collection은 컬렉션 참조를 돌려준다.
func (c *Client) Collection(p fspath.Path) (*firestore.CollectionRef, error) {
	full, err := c.resolve(p, fspath.KindCollection)
	if err != nil {
		return nil, err
	}
	return c.fs.Collection(full), nil
}

// Get은 문서를 읽는다. 없으면 ErrNotFound를 감싸 돌려준다.
func (c *Client) Get(ctx context.Context, p fspath.Path) (*firestore.DocumentSnapshot, error) {
	ref, err := c.Doc(p)
	if err != nil {
		return nil, err
	}
	snap, err := ref.Get(ctx)
	if err != nil {
		return nil, mapErr(err, p)
	}
	return snap, nil
}

// Set은 문서를 쓴다.
//
// 기본은 전체 덮어쓰기다. 병합이 필요하면 firestore.MergeAll을 넘긴다.
// IAP 원장의 sources 맵은 의도적으로 전체 덮어쓰기를 쓴다.
// 부분 병합하면 사라진 source가 남아 projection이 어긋난다.
func (c *Client) Set(ctx context.Context, p fspath.Path, data any, opts ...firestore.SetOption) error {
	ref, err := c.Doc(p)
	if err != nil {
		return err
	}
	if _, err := ref.Set(ctx, data, opts...); err != nil {
		return mapErr(err, p)
	}
	return nil
}

// Delete는 문서를 지운다.
//
// IAP 원장 문서는 삭제하지 않는다. 불변식 5다.
// 삭제가 허용된 건 완료 outbox뿐이다.
func (c *Client) Delete(ctx context.Context, p fspath.Path) error {
	ref, err := c.Doc(p)
	if err != nil {
		return err
	}
	if _, err := ref.Delete(ctx); err != nil {
		return mapErr(err, p)
	}
	return nil
}

// Tx는 트랜잭션 안에서의 접근점이다.
//
// Client와 같은 이유로 firestore.Transaction을 export하지 않는다.
type Tx struct {
	tx *firestore.Transaction
	c  *Client
}

// RunTransaction은 트랜잭션을 실행한다.
//
// Firestore는 충돌 시 fn을 자동으로 다시 부른다.
// 따라서 fn은 여러 번 실행될 수 있고 부작용이 없어야 한다.
// 트랜잭션 밖 상태를 fn 안에서 변경하지 않는다.
func (c *Client) RunTransaction(ctx context.Context, fn func(context.Context, *Tx) error) error {
	err := c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		return fn(ctx, &Tx{tx: tx, c: c})
	})
	if err != nil {
		if status.Code(err) == codes.Aborted {
			return fmt.Errorf("%w: %v", ErrAborted, err)
		}
		return err
	}
	return nil
}

// Get은 트랜잭션 안에서 문서를 읽는다.
//
// Firestore 트랜잭션은 모든 읽기가 쓰기보다 앞서야 한다.
// 쓰기 후에 읽으면 런타임 에러가 난다.
func (t *Tx) Get(p fspath.Path) (*firestore.DocumentSnapshot, error) {
	ref, err := t.c.Doc(p)
	if err != nil {
		return nil, err
	}
	snap, err := t.tx.Get(ref)
	if err != nil {
		return nil, mapErr(err, p)
	}
	return snap, nil
}

// Exists는 문서 존재 여부만 확인한다. 없어도 에러가 아니다.
func (t *Tx) Exists(p fspath.Path) (bool, *firestore.DocumentSnapshot, error) {
	snap, err := t.Get(p)
	if errors.Is(err, ErrNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return true, snap, nil
}

func (t *Tx) Set(p fspath.Path, data any, opts ...firestore.SetOption) error {
	ref, err := t.c.Doc(p)
	if err != nil {
		return err
	}
	return t.tx.Set(ref, data, opts...)
}

func (t *Tx) Create(p fspath.Path, data any) error {
	ref, err := t.c.Doc(p)
	if err != nil {
		return err
	}
	return t.tx.Create(ref, data)
}

func (t *Tx) Delete(p fspath.Path) error {
	ref, err := t.c.Doc(p)
	if err != nil {
		return err
	}
	return t.tx.Delete(ref)
}

// Query는 트랜잭션 안에서 컬렉션을 조회한다.
//
// build로 정렬·필터·상한을 붙인다. Firestore는 인덱스 없는 복합 쿼리가
// 런타임에 실패하므로 쓰는 쪽이 인덱스를 함께 선언해야 한다.
func (t *Tx) Query(
	p fspath.Path,
	build func(firestore.Query) firestore.Query,
) (*firestore.DocumentIterator, error) {
	col, err := t.c.Collection(p)
	if err != nil {
		return nil, err
	}
	q := col.Query
	if build != nil {
		q = build(q)
	}
	return t.tx.Documents(q), nil
}

// Query는 컬렉션을 조회한다.
func (c *Client) Query(
	ctx context.Context,
	p fspath.Path,
	build func(firestore.Query) firestore.Query,
) (*firestore.DocumentIterator, error) {
	col, err := c.Collection(p)
	if err != nil {
		return nil, err
	}
	q := col.Query
	if build != nil {
		q = build(q)
	}
	return q.Documents(ctx), nil
}

// IsDone은 iterator 종료 여부를 판정한다.
//
// 호출자가 google.golang.org/api/iterator를 직접 import하지 않아도 되게 한다.
// store가 Firestore 의존을 흡수한다는 원칙의 연장이다.
func IsDone(err error) bool { return errors.Is(err, iterator.Done) }

// mapErr는 gRPC 상태를 store 에러로 바꾼다.
//
// Firestore는 gRPC를 쓰므로 "없음"이 일반 에러로 온다.
// status.Code로 판정하지 않으면 네트워크 실패와 구분되지 않는다.
func mapErr(err error, p fspath.Path) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, p)
	case codes.Aborted:
		return fmt.Errorf("%w: %s", ErrAborted, p)
	default:
		return fmt.Errorf("store: %s 접근 실패: %w", p, err)
	}
}
