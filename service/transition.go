package service

import (
	"errors"
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/icon-project/goloop/service/transaction"

	"github.com/icon-project/goloop/service/contract"
	"github.com/icon-project/goloop/service/state"
	"github.com/icon-project/goloop/service/txresult"

	"github.com/icon-project/goloop/common/codec"
	"github.com/icon-project/goloop/service/eeproxy"

	"github.com/icon-project/goloop/common/db"
	"github.com/icon-project/goloop/module"
)

const (
	stepInited    = iota // parent, patch/normalTxes and state are ready.
	stepValidated        // Upon inited state, Txes are validated.
	stepValidating
	stepExecuting
	stepComplete // all information is ready. REMARK: InitTransition only has some result parts - result and nextValidators
	stepError    // fails validation or execution
	stepCanceled // canceled. requested to cancel after complete executione, just remain stepFinished
)

const (
	configUseParallelExecution = false
)

type transition struct {
	parent *transition
	bi     module.BlockInfo

	patchTransactions  module.TransactionList
	normalTransactions module.TransactionList

	db    db.Database
	cm    contract.ContractManager
	eem   eeproxy.Manager
	chain module.Chain

	cb module.TransitionCallback

	// internal processing state
	step  int
	mutex sync.Mutex

	result         []byte
	worldSnapshot  state.WorldSnapshot
	patchReceipts  module.ReceiptList
	normalReceipts module.ReceiptList
	logBloom       txresult.LogBloom
}

type transitionResult struct {
	StateHash         []byte
	PatchReceiptHash  []byte
	NormalReceiptHash []byte
}

func newTransitionResultFromBytes(bs []byte) (*transitionResult, error) {
	tresult := new(transitionResult)
	if _, err := codec.UnmarshalFromBytes(bs, tresult); err != nil {
		return nil, err
	}
	return tresult, nil
}

func (tr *transitionResult) Bytes() []byte {
	if bs, err := codec.MarshalToBytes(tr); err != nil {
		log.Panicf("Fail to marshal transitionResult")
		return nil
	} else {
		return bs
	}
}

func newTransition(parent *transition, patchtxs module.TransactionList,
	normaltxs module.TransactionList, bi module.BlockInfo, alreadyValidated bool,
) *transition {
	var step int
	if alreadyValidated {
		step = stepValidated
	} else {
		step = stepInited
	}

	if patchtxs == nil {
		patchtxs = transaction.NewTransactionListFromSlice(parent.db, nil)
	}
	if normaltxs == nil {
		normaltxs = transaction.NewTransactionListFromSlice(parent.db, nil)
	}
	return &transition{
		parent:             parent,
		bi:                 bi,
		patchTransactions:  patchtxs,
		normalTransactions: normaltxs,
		db:                 parent.db,
		cm:                 parent.cm,
		eem:                parent.eem,
		step:               step,
		chain:              parent.chain,
	}
}

// all parameters should be valid.
func newInitTransition(db db.Database, result []byte,
	validatorList module.ValidatorList, cm contract.ContractManager,
	em eeproxy.Manager, chain module.Chain,
) (*transition, error) {
	var tresult transitionResult
	if len(result) > 0 {
		if _, err := codec.UnmarshalFromBytes(result, &tresult); err != nil {
			return nil, err
		}
	}
	ws := state.NewWorldState(db, tresult.StateHash, validatorList)

	return &transition{
		patchTransactions:  transaction.NewTransactionListFromSlice(db, nil),
		normalTransactions: transaction.NewTransactionListFromSlice(db, nil),
		bi:                 &blockInfo{int64(0), int64(0)},
		db:                 db,
		cm:                 cm,
		eem:                em,
		step:               stepComplete,
		worldSnapshot:      ws.GetSnapshot(),
		chain:              chain,
	}, nil
}

func (t *transition) PatchTransactions() module.TransactionList {
	return t.patchTransactions
}
func (t *transition) NormalTransactions() module.TransactionList {
	return t.normalTransactions
}

// Execute executes this transition.
// The result is asynchronously notified by cb. canceler can be used
// to cancel it after calling Execute. After canceler returns true,
// all succeeding cb functions may not be called back.
// REMARK: It is assumed to be called once. Any additional call returns
// error.
func (t *transition) Execute(cb module.TransitionCallback) (canceler func() bool, err error) {
	t.mutex.Lock()

	switch t.step {
	case stepInited:
		t.step = stepValidating
	case stepValidated:
		// when this transition created by this node
		t.step = stepExecuting
	default:
		return nil, errors.New("Invalid transition state: " + t.stepString())
	}
	t.cb = cb
	go t.executeSync(t.step == stepExecuting)

	t.mutex.Unlock()

	return t.cancelExecution, nil
}

// Result returns service manager defined result bytes.
func (t *transition) Result() []byte {
	if t.step != stepComplete {
		return nil
	}
	return t.result
}

// NextValidators returns the addresses of validators as a result of
// transaction processing.
// It may return nil before cb.OnExecute is called back by Execute.
func (t *transition) NextValidators() module.ValidatorList {
	if t.worldSnapshot != nil {
		return t.worldSnapshot.GetValidatorSnapshot()
	}
	log.Printf("Fail to get valid Validators")
	return nil
}

// LogBloom returns log bloom filter for this transition.
// It may return nil before cb.OnExecute is called back by Execute.
func (t *transition) LogBloom() module.LogBloom {
	if t.step != stepComplete {
		return nil
	}
	return &t.logBloom
}

func (t *transition) newWorldContext() state.WorldContext {
	var ws state.WorldState
	if t.parent != nil {
		var err error
		ws, err = state.WorldStateFromSnapshot(t.parent.worldSnapshot)
		if err != nil {
			log.Panicf("Fail to build world state from snapshot err=%+v", err)
		}
	} else {
		ws = state.NewWorldState(t.db, nil, nil)
	}
	return state.NewWorldContext(ws, t.bi)
}

func (t *transition) executeSync(alreadyValidated bool) {
	var normalCount, patchCount int
	if !alreadyValidated {
		var ok bool
		wc := t.newWorldContext()
		ok, patchCount = t.validateTxs(t.patchTransactions, wc)
		if !ok {
			return
		}
		ok, normalCount = t.validateTxs(t.normalTransactions, wc)
		if !ok {
			return
		}
		if t.cb != nil {
			t.cb.OnValidate(t, nil)
		}
	} else {
		for i := t.patchTransactions.Iterator(); i.Has(); i.Next() {
			patchCount++
		}
		for i := t.normalTransactions.Iterator(); i.Has(); i.Next() {
			normalCount++
		}
		if t.cb != nil {
			t.cb.OnValidate(t, nil)
		}
	}

	t.mutex.Lock()
	t.step = stepExecuting
	t.mutex.Unlock()

	ctx := contract.NewContext(t.newWorldContext(), t.cm, t.eem, t.chain)

	startTime := time.Now()

	patchReceipts := make([]txresult.Receipt, patchCount)
	t.executeTxs(t.patchTransactions, ctx, patchReceipts)
	normalReceipts := make([]txresult.Receipt, normalCount)
	t.executeTxs(t.normalTransactions, ctx, normalReceipts)

	cumulativeSteps := big.NewInt(0)
	gatheredFee := big.NewInt(0)
	fee := big.NewInt(0)

	for _, receipts := range [][]txresult.Receipt{patchReceipts, normalReceipts} {
		for _, r := range receipts {
			used := r.StepUsed()
			cumulativeSteps.Add(cumulativeSteps, used)
			r.SetCumulativeStepUsed(cumulativeSteps)

			fee.Mul(r.StepPrice(), used)
			gatheredFee.Add(gatheredFee, fee)
		}
	}
	t.patchReceipts = txresult.NewReceiptListFromSlice(t.db, patchReceipts)
	t.normalReceipts = txresult.NewReceiptListFromSlice(t.db, normalReceipts)

	// save gathered fee to treasury
	tr := ctx.GetAccountState(ctx.Treasury().ID())
	trbal := tr.GetBalance()
	trbal.Add(trbal, gatheredFee)
	tr.SetBalance(trbal)

	t.worldSnapshot = ctx.GetSnapshot()

	elapsed := float64(time.Now().Sub(startTime)/time.Microsecond) / 1000
	log.Printf("Transactions: %6d  Elapsed: %7.3f msecs  TPS: %9.2f",
		patchCount+normalCount, elapsed, float64(patchCount+normalCount)/elapsed*1000)

	tresult := transitionResult{
		t.worldSnapshot.StateHash(),
		t.patchReceipts.Hash(),
		t.normalReceipts.Hash(),
	}
	t.result = tresult.Bytes()

	t.mutex.Lock()
	t.step = stepComplete
	t.mutex.Unlock()
	if t.cb != nil {
		t.cb.OnExecute(t, nil)
	}
}

func (t *transition) validateTxs(l module.TransactionList, wc state.WorldContext) (bool, int) {
	if l == nil {
		return true, 0
	}
	cnt := 0
	for i := l.Iterator(); i.Has(); i.Next() {
		if t.step == stepCanceled {
			return false, 0
		}

		txi, _, err := i.Get()
		if err != nil {
			log.Panicf("Fail to iterate transaction list err=%+v", err)
		}

		if err := txi.(transaction.Transaction).PreValidate(wc, true); err != nil {
			t.mutex.Lock()
			t.step = stepError
			t.mutex.Unlock()
			t.cb.OnValidate(t, err)
			return false, 0
		}
		cnt += 1
	}
	return true, cnt
}

func (t *transition) executeTxs(l module.TransactionList, ctx contract.Context, rctBuf []txresult.Receipt) (bool, int) {
	if l == nil {
		return true, 0
	}
	cnt := 0
	for i := l.Iterator(); i.Has(); i.Next() {
		if t.step == stepCanceled {
			return false, 0
		}
		txi, _, err := i.Get()
		if err != nil {
			log.Panicf("Fail to iterate transaction list err=%+v", err)
		}
		txo := txi.(transaction.Transaction)
		txh, err := txo.GetHandler(t.cm)
		if err != nil {
			log.Panicf("Fail to handle transaction for %+v", err)
		}
		if configUseParallelExecution {
			wc, err := txh.Prepare(ctx)
			if err != nil {
				log.Panicf("Fail to prepare for %+v", err)
			}
			ctx = contract.NewContext(wc, t.cm, t.eem, t.chain)
			ctx.SetTransactionInfo(&state.TransactionInfo{
				Index:     int32(cnt),
				Timestamp: txo.Timestamp(),
				Nonce:     txo.Nonce(),
				Hash:      txo.ID(),
				From:      txo.From(),
			})
			go func(ctx contract.Context, rb *txresult.Receipt) {
				wvs := ctx.WorldVirtualState()
				wvs.Begin()
				if rct, err := txh.Execute(ctx); err != nil {
					log.Panicf("Fail to execute transaction err=%+v", err)
				} else {
					*rb = rct
				}
				txh.Dispose()
				wvs.Commit()
			}(ctx, &rctBuf[cnt])
		} else {
			ctx.SetTransactionInfo(&state.TransactionInfo{
				Index:     int32(cnt),
				Timestamp: txo.Timestamp(),
				Nonce:     txo.Nonce(),
				Hash:      txo.ID(),
				From:      txo.From(),
			})
			if logDebug {
				log.Printf("START TX <0x%x>", txo.ID())
			}
			ctx.ClearCache()
			if rct, err := txh.Execute(ctx); err != nil {
				log.Panicf("Fail to execute transaction err=%+v", err)
			} else {
				rctBuf[cnt] = rct
			}
			if logDebug {
				log.Printf("END   TX <0x%x>", txo.ID())
			}
			txh.Dispose()
		}
		cnt++
	}
	if wvs := ctx.WorldVirtualState(); wvs != nil {
		wvs.Realize()
	}
	return true, cnt
}

func (t *transition) finalizeNormalTransaction() {
	t.normalTransactions.Flush()
}

func (t *transition) finalizePatchTransaction() {
	t.patchTransactions.Flush()
}

func (t *transition) finalizeResult() {
	t.worldSnapshot.Flush()
	t.patchReceipts.Flush()
	t.normalReceipts.Flush()
	t.parent = nil
}

func (t *transition) cancelExecution() bool {
	t.mutex.Lock()
	if t.step != stepComplete && t.step != stepError {
		t.step = stepCanceled
	}
	t.mutex.Unlock()
	return true
}

func (t *transition) stepString() string {
	switch t.step {
	case stepInited:
		return "Inited"
	case stepValidated:
		return "Validated"
	case stepValidating:
		return "Validating"
	case stepExecuting:
		return "Executing"
	case stepComplete:
		return "Executed"
	case stepCanceled:
		return "Canceled"
	default:
		return ""
	}
}
