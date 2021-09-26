package through

import (
	"testing"
	"time"
)

func TestTimeDiff(t *testing.T) {

	/**
	如果 自己是 100  对方是 80 ， 说明对方比自己的时间要慢， 那么时间差就会为负数
	如果 自己是 100   对方是 120 ， 说明对方比自己的时间要快， 那么时间差就会为正数
	不管正数还是负数，只要用时间 + 时间差 就是 双方同步好的时间
	*/
	t.Log(maxTimeDiff(-20,10))

	baseTime := time.Now().Unix()
	client1Date := baseTime + 20;
	client2Date := baseTime - 20;

	client1TimeDiff := baseTime - client1Date
	client2TimeDiff := baseTime - client2Date

	t.Log("客户端1的时间为:",client1Date," 时差:",client1TimeDiff)
	t.Log("客户端2的时间为:",client2Date," 时差:",client2TimeDiff)

	baseTime += 5  //增加5秒的缓冲时间
	t.Log("约定时间增加5秒缓冲后:",baseTime)


	t.Log("客户端1的约定发送时间为:",timeDiffToUnix(baseTime,client1TimeDiff))
	t.Log("客户端2的约定发送时间为:",timeDiffToUnix(baseTime,client2TimeDiff))
}
